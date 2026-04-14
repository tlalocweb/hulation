package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// executor runs parsed commands inside the build container.
type executor struct {
	proto   *protocolIO
	workdir string
}

func newExecutor(proto *protocolIO) *executor {
	return &executor{
		proto:   proto,
		workdir: "/builder",
	}
}

// run executes a single command.
func (e *executor) run(name, args string) error {
	switch name {
	case "WORKDIR":
		return e.cmdWorkdir(args)
	case "HUGO":
		return e.cmdStaticGen("hugo", args)
	case "ASTRO":
		return e.cmdStaticGen("astro", args)
	case "GATSBY":
		return e.cmdStaticGen("gatsby", args)
	case "MKDOCS":
		return e.cmdStaticGen("mkdocs", args)
	case "CP":
		return e.cmdCp(args)
	case "RM":
		return e.cmdRm(args)
	case "RUN":
		return e.cmdRun(args)
	case "FINALIZE":
		return e.cmdFinalize(args)
	default:
		return fmt.Errorf("unknown command: %s", name)
	}
}

// cmdWorkdir sets the working directory and initiates tarball transfer.
func (e *executor) cmdWorkdir(dir string) error {
	if dir == "" {
		dir = "/builder"
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("WORKDIR path must be absolute: %s", dir)
	}
	e.workdir = filepath.Clean(dir)

	// Ensure the directory exists
	if err := os.MkdirAll(e.workdir, 0o755); err != nil {
		return fmt.Errorf("creating workdir %s: %w", e.workdir, err)
	}

	// Ask hula to send the site source tarball
	if err := e.proto.send(msgSendTarballTo, e.workdir); err != nil {
		return fmt.Errorf("sending SEND_TARBALL_TO: %w", err)
	}

	e.proto.sendLog("Waiting for site source tarball...")

	// Wait for hula to confirm the tarball has been copied in
	_, err := e.proto.waitFor(msgInboundTarballReady)
	if err != nil {
		return fmt.Errorf("waiting for INBOUND_TARBALL_READY: %w", err)
	}

	// The tarball should be at <workdir>/site-source.tar.gz, placed by hula via docker cp.
	// Extract it to <workdir>/site/
	tarballPath := filepath.Join(e.workdir, "site-source.tar.gz")
	siteDir := filepath.Join(e.workdir, "site")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		return fmt.Errorf("creating site dir: %w", err)
	}

	if err := extractTarGz(tarballPath, siteDir); err != nil {
		return fmt.Errorf("extracting site source: %w", err)
	}
	// Remove the tarball after extraction
	os.Remove(tarballPath)

	e.proto.sendLog("Site source extracted to %s", siteDir)
	return nil
}

// cmdStaticGen runs a static site generator (hugo, astro, gatsby, mkdocs).
func (e *executor) cmdStaticGen(generator, args string) error {
	e.proto.sendLog("Running %s %s", generator, args)

	// Determine the site directory
	siteDir := filepath.Join(e.workdir, "site")

	// Build the command
	var cmdArgs []string
	if args != "" {
		cmdArgs = splitArgs(args)
	}

	cmd := exec.Command(generator, cmdArgs...)
	cmd.Dir = siteDir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		// Send build output as log lines
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" {
				e.proto.sendLog("[%s] %s", generator, line)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w", generator, err)
	}
	e.proto.sendLog("%s completed successfully", generator)
	return nil
}

// cmdCp copies files, sandboxed to the workdir.
func (e *executor) cmdCp(args string) error {
	e.proto.sendLog("CP %s", args)

	// We sanitize to ensure all paths are within the workdir.
	// Use shell cp but prepend workdir to relative paths.
	siteDir := filepath.Join(e.workdir, "site")
	cmdStr := fmt.Sprintf("cd %s && cp %s", shellQuote(siteDir), args)

	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = siteDir
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		e.proto.sendLog("[cp] %s", strings.TrimSpace(string(output)))
	}
	if err != nil {
		return fmt.Errorf("CP failed: %w", err)
	}
	return nil
}

// cmdRm removes files, sandboxed to the workdir.
func (e *executor) cmdRm(args string) error {
	e.proto.sendLog("RM %s", args)

	siteDir := filepath.Join(e.workdir, "site")

	// Validate no path escapes the workdir
	parts := splitArgs(args)
	for _, p := range parts {
		if strings.HasPrefix(p, "-") {
			continue // flags like -rf
		}
		resolved := p
		if !filepath.IsAbs(p) {
			resolved = filepath.Join(siteDir, p)
		}
		resolved = filepath.Clean(resolved)
		if !strings.HasPrefix(resolved, e.workdir) {
			return fmt.Errorf("RM path %q escapes workdir %s", p, e.workdir)
		}
	}

	cmdStr := fmt.Sprintf("cd %s && rm %s", shellQuote(siteDir), args)
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = siteDir
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		e.proto.sendLog("[rm] %s", strings.TrimSpace(string(output)))
	}
	if err != nil {
		return fmt.Errorf("RM failed: %w", err)
	}
	return nil
}

// cmdRun runs an arbitrary shell command in the site directory.
func (e *executor) cmdRun(args string) error {
	e.proto.sendLog("RUN %s", args)

	siteDir := filepath.Join(e.workdir, "site")
	cmd := exec.Command("sh", "-c", args)
	cmd.Dir = siteDir
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" {
				e.proto.sendLog("[run] %s", line)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("RUN failed: %w", err)
	}
	return nil
}

// cmdFinalize tarballs the specified folder and notifies hula.
func (e *executor) cmdFinalize(folder string) error {
	// Resolve the folder relative to the site dir
	siteDir := filepath.Join(e.workdir, "site")
	targetDir := folder
	if !filepath.IsAbs(folder) {
		targetDir = filepath.Join(siteDir, folder)
	}
	targetDir = filepath.Clean(targetDir)

	// Validate target is within workdir
	if !strings.HasPrefix(targetDir, e.workdir) {
		return fmt.Errorf("FINALIZE path %q escapes workdir %s", folder, e.workdir)
	}

	// Verify directory exists
	info, err := os.Stat(targetDir)
	if err != nil {
		return fmt.Errorf("FINALIZE directory %s: %w", targetDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("FINALIZE target %s is not a directory", targetDir)
	}

	e.proto.sendLog("Finalizing %s ...", targetDir)

	// Create tarball
	tarballPath := filepath.Join(e.workdir, "site-output.tar.gz")
	if err := createTarGz(tarballPath, targetDir); err != nil {
		return fmt.Errorf("creating output tarball: %w", err)
	}

	e.proto.sendLog("Output tarball created at %s", tarballPath)

	// Tell hula the tarball is ready
	if err := e.proto.send(msgOutboundTarballReady, tarballPath); err != nil {
		return fmt.Errorf("sending OUTBOUND_TARBALL_READY: %w", err)
	}

	return nil
}

// createTarGz creates a gzipped tar archive of the given directory.
func createTarGz(tarballPath, sourceDir string) error {
	f, err := os.Create(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Create tar header
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		// Handle symlinks
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			header.Linkname = link
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		// Write file content
		if !info.IsDir() && info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := io.Copy(tw, file); err != nil {
				return err
			}
		}

		return nil
	})
}

// extractTarGz extracts a gzipped tar archive to the given directory.
func extractTarGz(tarballPath, destDir string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)
		// Security: prevent path traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			return fmt.Errorf("tar entry %s escapes destination %s", header.Name, destDir)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitArgs splits a string into arguments, respecting basic quoting.
func splitArgs(s string) []string {
	return strings.Fields(s)
}

// shellQuote quotes a string for safe use in a shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

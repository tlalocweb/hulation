package sitedeploy

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/tlalocweb/hulation/log"
)

// BuilderContainer manages the lifecycle of an ephemeral builder container.
type BuilderContainer struct {
	cli         *client.Client
	containerID string
	imageName   string
}

// newBuilderContainer creates a new builder container manager.
func newBuilderContainer(cli *client.Client) *BuilderContainer {
	return &BuilderContainer{cli: cli}
}

// ensureImage checks that the builder image exists locally.
func (bc *BuilderContainer) ensureImage(ctx context.Context, imageName string) error {
	_, _, err := bc.cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return fmt.Errorf("builder image %q not found locally. Load it with: docker load < %s.tar.gz", imageName, imageName)
	}
	bc.imageName = imageName
	return nil
}

// buildDerivedImage builds a derived Docker image from the base builder image
// with the given prebuild Dockerfile commands. Returns the derived image name.
// Derived images are cached by content hash.
func (bc *BuilderContainer) buildDerivedImage(ctx context.Context, baseImage, prebuildCommands string) (string, error) {
	// Compute hash of prebuild content for caching
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(baseImage+"\n"+prebuildCommands)))[:16]
	derivedName := "hula-builder-derived-" + hash

	// Check if derived image already exists
	_, _, err := bc.cli.ImageInspectWithRaw(ctx, derivedName)
	if err == nil {
		log.Infof("sitedeploy: using cached derived image %s", derivedName)
		bc.imageName = derivedName
		return derivedName, nil
	}

	log.Infof("sitedeploy: building derived image %s from %s", derivedName, baseImage)

	// Create Dockerfile content
	dockerfile := fmt.Sprintf("FROM %s\n%s\n", baseImage, prebuildCommands)

	// Create build context as tar
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfile)),
		Mode: 0o644,
	}); err != nil {
		return "", fmt.Errorf("creating build context: %w", err)
	}
	if _, err := tw.Write([]byte(dockerfile)); err != nil {
		return "", fmt.Errorf("writing Dockerfile to tar: %w", err)
	}
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("closing tar: %w", err)
	}

	// Build the image
	resp, err := bc.cli.ImageBuild(ctx, &buf, build.ImageBuildOptions{
		Tags:       []string{derivedName},
		Dockerfile: "Dockerfile",
		Remove:     true,
		NoCache:    false,
	})
	if err != nil {
		return "", fmt.Errorf("building derived image: %w", err)
	}
	defer resp.Body.Close()

	// Drain output to complete the build
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		log.Debugf("sitedeploy: docker build: %s", line)
	}

	bc.imageName = derivedName
	return derivedName, nil
}

// startContainer creates and starts the builder container with hulabuild as entrypoint.
// It returns the attached connection (for writing stdin) and the stdout reader.
// The caller must close the connection when done.
func (bc *BuilderContainer) startContainer(ctx context.Context, commandList string, env []string) (conn io.WriteCloser, stdout *bufio.Reader, err error) {
	containerName := "hula-builder-" + randomSuffix()

	cfg := &container.Config{
		Image:        bc.imageName,
		Entrypoint:   []string{"/usr/local/bin/hulabuild"},
		Env:          env,
		OpenStdin:    true,
		StdinOnce:    false,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	}

	hostCfg := &container.HostConfig{}

	resp, err := bc.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, containerName)
	if err != nil {
		return nil, nil, fmt.Errorf("creating builder container: %w", err)
	}
	bc.containerID = resp.ID

	// Attach to the container to get stdin/stdout streams
	attachResp, err := bc.cli.ContainerAttach(ctx, bc.containerID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		bc.cleanup(ctx)
		return nil, nil, fmt.Errorf("attaching to builder container: %w", err)
	}

	// Start the container
	if err := bc.cli.ContainerStart(ctx, bc.containerID, container.StartOptions{}); err != nil {
		attachResp.Close()
		bc.cleanup(ctx)
		return nil, nil, fmt.Errorf("starting builder container: %w", err)
	}

	log.Infof("sitedeploy: builder container %s started (id=%s)", containerName, bc.containerID[:12])

	return attachResp.Conn, attachResp.Reader, nil
}

// copyToContainer copies a tarball into the container at the specified path.
func (bc *BuilderContainer) copyToContainer(ctx context.Context, destPath string, content io.Reader) error {
	return bc.cli.CopyToContainer(ctx, bc.containerID, destPath, content, container.CopyToContainerOptions{})
}

// copyFromContainer extracts a file/directory from the container.
func (bc *BuilderContainer) copyFromContainer(ctx context.Context, srcPath string) (io.ReadCloser, error) {
	reader, _, err := bc.cli.CopyFromContainer(ctx, bc.containerID, srcPath)
	if err != nil {
		return nil, fmt.Errorf("copying from container path %s: %w", srcPath, err)
	}
	return reader, nil
}

// cleanup stops and removes the builder container.
func (bc *BuilderContainer) cleanup(ctx context.Context) {
	if bc.containerID == "" {
		return
	}
	timeout := 5
	_ = bc.cli.ContainerStop(ctx, bc.containerID, container.StopOptions{Timeout: &timeout})
	err := bc.cli.ContainerRemove(ctx, bc.containerID, container.RemoveOptions{Force: true})
	if err != nil {
		log.Warnf("sitedeploy: failed to remove builder container %s: %s", bc.containerID[:12], err)
	} else {
		log.Infof("sitedeploy: removed builder container %s", bc.containerID[:12])
	}
	bc.containerID = ""
}

// createSourceTarball creates a gzipped tar of the site source directory
// suitable for CopyToContainer. The tar contains a single entry "site-source.tar.gz"
// which itself is a tar.gz of the source directory contents.
func createSourceTarball(sourceDir string) (io.Reader, error) {
	// First create the inner tar.gz of the source directory
	var innerBuf bytes.Buffer
	gw := gzip.NewWriter(&innerBuf)
	tw := tar.NewWriter(gw)

	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
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

		if !info.IsDir() && info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking source dir: %w", err)
	}
	tw.Close()
	gw.Close()

	// Now wrap it in an outer tar for docker cp (which expects a tar stream)
	var outerBuf bytes.Buffer
	otw := tar.NewWriter(&outerBuf)
	if err := otw.WriteHeader(&tar.Header{
		Name: "site-source.tar.gz",
		Size: int64(innerBuf.Len()),
		Mode: 0o644,
	}); err != nil {
		return nil, err
	}
	if _, err := otw.Write(innerBuf.Bytes()); err != nil {
		return nil, err
	}
	otw.Close()

	return &outerBuf, nil
}

// extractSiteTarball extracts the built site from the container tarball stream
// (as returned by CopyFromContainer) into the destination directory.
func extractSiteTarball(reader io.Reader, destDir string) error {
	// CopyFromContainer returns a tar stream. The first entry should be our tar.gz file.
	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading container tar stream: %w", err)
		}

		// We expect a single file: the output tar.gz
		if strings.HasSuffix(header.Name, ".tar.gz") || strings.HasSuffix(header.Name, ".tgz") {
			return extractTarGzFromReader(tr, destDir)
		}

		// If it's a directory or other file type, try to extract directly
		target := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			continue // skip path traversal attempts
		}

		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, os.FileMode(header.Mode))
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0o755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			io.Copy(f, tr)
			f.Close()
		}
	}
	return nil
}

// extractTarGzFromReader extracts a gzipped tar stream into a directory.
func extractTarGzFromReader(r io.Reader, destDir string) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
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
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			continue
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
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			os.Symlink(header.Linkname, target)
		}
	}
	return nil
}

// randomSuffix returns a short random string for container naming.
func randomSuffix() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d-%d-%d",
		os.Getpid(), os.Getuid(), time.Now().UnixNano()))))[:12]
}

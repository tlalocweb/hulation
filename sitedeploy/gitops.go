package sitedeploy

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

// CloneOrPull clones the repo if the target directory does not exist,
// or pulls updates if it does. Returns the path to the repo directory.
func CloneOrPull(gad *config.GitAutoDeployConfig) (string, error) {
	repoDir := gad.DataDir

	// Build the authenticated URL if credentials are provided
	repoURL, err := buildAuthURL(gad.Repo, gad.Creds)
	if err != nil {
		return "", fmt.Errorf("building repo URL: %w", err)
	}

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		// Fresh clone
		log.Infof("sitedeploy: cloning %s to %s", sanitizeURL(gad.Repo), repoDir)
		if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
			return "", fmt.Errorf("creating parent dir: %w", err)
		}
		if err := gitClone(repoURL, repoDir, &gad.Ref); err != nil {
			return "", fmt.Errorf("git clone: %w", err)
		}
	} else {
		// Pull updates
		log.Infof("sitedeploy: pulling updates in %s", repoDir)
		if err := gitPull(repoURL, repoDir, &gad.Ref); err != nil {
			return "", fmt.Errorf("git pull: %w", err)
		}
	}

	return repoDir, nil
}

// gitClone performs a fresh clone of the repository.
func gitClone(repoURL, targetDir string, ref *config.GitRefConfig) error {
	args := []string{"clone", "--depth", "1"}

	if ref.Branch != "" {
		args = append(args, "--branch", ref.Branch)
	}
	// For tag refs, we clone and then checkout the appropriate tag
	args = append(args, repoURL, targetDir)

	if err := runGit("", args...); err != nil {
		return err
	}

	// If tag ref, checkout the right tag
	if ref.Tag != "" {
		return checkoutTag(targetDir, ref.Tag)
	}
	return nil
}

// gitPull fetches and checks out the appropriate ref.
func gitPull(repoURL, repoDir string, ref *config.GitRefConfig) error {
	// Update the remote URL in case credentials changed
	_ = runGit(repoDir, "remote", "set-url", "origin", repoURL)

	if ref.Branch != "" {
		// Fetch the branch and reset to it
		if err := runGit(repoDir, "fetch", "--depth", "1", "origin", ref.Branch); err != nil {
			return fmt.Errorf("fetch branch %s: %w", ref.Branch, err)
		}
		if err := runGit(repoDir, "checkout", ref.Branch); err != nil {
			return fmt.Errorf("checkout branch %s: %w", ref.Branch, err)
		}
		if err := runGit(repoDir, "reset", "--hard", "origin/"+ref.Branch); err != nil {
			return fmt.Errorf("reset to origin/%s: %w", ref.Branch, err)
		}
		return nil
	}

	if ref.Tag != "" {
		// Fetch all tags
		if err := runGit(repoDir, "fetch", "--tags", "--force", "origin"); err != nil {
			return fmt.Errorf("fetch tags: %w", err)
		}
		return checkoutTag(repoDir, ref.Tag)
	}

	return fmt.Errorf("no branch or tag specified in ref config")
}

// checkoutTag resolves and checks out the appropriate tag based on the tag config.
func checkoutTag(repoDir, tagRef string) error {
	// List all tags
	output, err := runGitOutput(repoDir, "tag", "-l")
	if err != nil {
		return fmt.Errorf("listing tags: %w", err)
	}

	tags := strings.Split(strings.TrimSpace(output), "\n")
	var matchedTag string

	switch strings.ToLower(tagRef) {
	case "semver":
		// Find the highest semver tag
		matchedTag = findHighestSemverTag(tags)
		if matchedTag == "" {
			return fmt.Errorf("no valid semver tags found")
		}
	case "any":
		// Use the most recent tag (last in list)
		if len(tags) == 0 || (len(tags) == 1 && tags[0] == "") {
			return fmt.Errorf("no tags found")
		}
		matchedTag = tags[len(tags)-1]
	default:
		// Exact tag match
		found := false
		for _, t := range tags {
			if t == tagRef {
				found = true
				matchedTag = tagRef
				break
			}
		}
		if !found {
			return fmt.Errorf("tag %q not found", tagRef)
		}
	}

	log.Infof("sitedeploy: checking out tag %s", matchedTag)
	return runGit(repoDir, "checkout", "tags/"+matchedTag)
}

// findHighestSemverTag returns the highest valid semver tag from a list.
func findHighestSemverTag(tags []string) string {
	var semverTags []string
	for _, t := range tags {
		if MatchesTagRef(t, "semver") {
			semverTags = append(semverTags, t)
		}
	}
	if len(semverTags) == 0 {
		return ""
	}

	sort.Slice(semverTags, func(i, j int) bool {
		cmp, err := CompareSemverTags(semverTags[i], semverTags[j])
		if err != nil {
			return false
		}
		return cmp < 0
	})

	return semverTags[len(semverTags)-1]
}

// buildAuthURL embeds credentials into the repo URL if provided.
//
// Provider-agnostic: works for github.com, gitlab.com, bitbucket.org, or any
// HTTPS git host. Conventions:
//   - GitHub PATs: username can be anything (e.g. "x-access-token"), password is the PAT
//   - GitLab PATs: username "oauth2", password is the PAT
//   - Bitbucket app passwords: username is the bitbucket username, password is the app pwd
//
// A partial creds block (one field set, the other empty) is rejected here
// rather than silently dropped — git would then try to prompt and fail
// confusingly with `terminal prompts disabled`.
func buildAuthURL(repoURL string, creds *config.GitCredentials) (string, error) {
	if creds == nil || (creds.Username == "" && creds.Password == "") {
		return repoURL, nil
	}

	u, err := url.Parse(repoURL)
	if err != nil {
		return "", fmt.Errorf("parsing repo URL: %w", err)
	}

	switch {
	case creds.Username != "" && creds.Password != "":
		u.User = url.UserPassword(creds.Username, creds.Password)
	case creds.Password != "":
		// Token-only auth (common for GitHub).
		u.User = url.UserPassword("x-access-token", creds.Password)
	default:
		// Username set but no password — almost certainly a misconfigured
		// `{{env:VAR}}` template that resolved to empty.
		return "", fmt.Errorf("creds.username=%q is set but creds.password is empty (env var unset?)", creds.Username)
	}

	return u.String(), nil
}

// sanitizeURL removes credentials from a URL for safe logging.
func sanitizeURL(repoURL string) string {
	u, err := url.Parse(repoURL)
	if err != nil {
		return repoURL
	}
	u.User = nil
	return u.String()
}

// runGit runs a git command in the given directory.
func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Sanitize output to remove any credentials
		sanitized := sanitizeGitOutput(string(output))
		return fmt.Errorf("git %s: %w: %s", strings.Join(args[:min(len(args), 2)], " "), err, sanitized)
	}
	return nil
}

// runGitOutput runs a git command and returns its stdout.
func runGitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args[:min(len(args), 2)], " "), err)
	}
	return string(output), nil
}

// sanitizeGitOutput scrubs potential credentials from git output.
func sanitizeGitOutput(output string) string {
	// Remove anything that looks like credentials in a URL
	// Pattern: ://user:password@ -> ://***@
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "://"); idx >= 0 {
			if atIdx := strings.Index(line[idx:], "@"); atIdx >= 0 {
				lines[i] = line[:idx] + "://***" + line[idx+atIdx:]
			}
		}
	}
	return strings.Join(lines, "\n")
}

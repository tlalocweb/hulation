package sitedeploy

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	gogittransport "github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

// CloneOrPull clones the repo if the target directory does not exist,
// or pulls updates if it does. Returns the path to the repo directory.
func CloneOrPull(gad *config.GitAutoDeployConfig) (string, error) {
	return CloneOrPullTo(gad, gad.DataDir)
}

// CloneOrPullTo is the same as CloneOrPull but lets the caller pick the
// target directory. Used by the staging boot path to land the working
// tree directly in StagingSrcDir (so WebDAV edits, `hulactl stage`, etc.
// operate on a real git working tree). The standard production path
// keeps using gad.DataDir via CloneOrPull.
func CloneOrPullTo(gad *config.GitAutoDeployConfig, repoDir string) (string, error) {
	if repoDir == "" {
		return "", fmt.Errorf("repoDir is empty")
	}

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		log.Infof("sitedeploy: cloning %s to %s", sanitizeURL(gad.Repo), repoDir)
		if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
			return "", fmt.Errorf("creating parent dir: %w", err)
		}
		if err := gitClone(gad, repoDir); err != nil {
			return "", fmt.Errorf("git clone: %w", err)
		}
	} else {
		log.Infof("sitedeploy: pulling updates in %s", repoDir)
		if err := gitPull(gad, repoDir); err != nil {
			return "", fmt.Errorf("git pull: %w", err)
		}
	}

	return repoDir, nil
}

// IsGitWorkingTree returns true when dir contains a .git entry (file or
// directory). Used by the staging git verbs to refuse cleanly when the
// caller points at a non-repo directory (e.g. a stale install where
// CloneOrPull was never run).
func IsGitWorkingTree(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	return true
}

// gitClone performs a fresh shallow clone of the repository onto
// targetDir. When ref.Branch is set we clone that branch only; when
// ref.Tag is set we clone defaults and then check out the matching
// tag.
func gitClone(gad *config.GitAutoDeployConfig, targetDir string) error {
	auth, err := buildAuth(gad.Creds)
	if err != nil {
		return err
	}
	opts := &gogit.CloneOptions{
		URL:      gad.Repo,
		Auth:     auth,
		Depth:    1,
		Progress: nil,
	}
	if gad.Ref.Branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(gad.Ref.Branch)
		opts.SingleBranch = true
	}
	repo, err := gogit.PlainClone(targetDir, false, opts)
	if err != nil {
		return fmt.Errorf("clone: %w", sanitizeAuthErr(err))
	}

	if gad.Ref.Tag != "" {
		return checkoutTag(repo, targetDir, gad.Ref.Tag, gad)
	}
	return nil
}

// gitPull fetches the configured ref and resets the working tree to
// it. Equivalent to the previous shell-out flow:
//
//	git remote set-url origin <repo>
//	git fetch --depth 1 origin <branch>
//	git reset --hard origin/<branch>
//
// The reset model is intentional — the production CloneOrPull is
// driven from boot time and doesn't carry uncommitted edits; we want a
// guaranteed match to upstream rather than a merge-or-bust pull.
func gitPull(gad *config.GitAutoDeployConfig, repoDir string) error {
	repo, err := gogit.PlainOpen(repoDir)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	if err := setOriginURL(repo, gad.Repo); err != nil {
		return err
	}
	auth, err := buildAuth(gad.Creds)
	if err != nil {
		return err
	}

	if gad.Ref.Branch != "" {
		refSpec := gogitcfg.RefSpec(fmt.Sprintf(
			"+refs/heads/%s:refs/remotes/origin/%s",
			gad.Ref.Branch, gad.Ref.Branch,
		))
		fetchOpts := &gogit.FetchOptions{
			RemoteName: "origin",
			Auth:       auth,
			Depth:      1,
			Force:      true,
			RefSpecs:   []gogitcfg.RefSpec{refSpec},
		}
		if err := repo.Fetch(fetchOpts); err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
			return fmt.Errorf("fetch branch %s: %w", gad.Ref.Branch, sanitizeAuthErr(err))
		}
		// Resolve origin/<branch> and hard-reset the worktree onto it.
		// Equivalent to `git reset --hard origin/<branch>`.
		remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", gad.Ref.Branch), true)
		if err != nil {
			return fmt.Errorf("resolve origin/%s: %w", gad.Ref.Branch, err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return fmt.Errorf("worktree: %w", err)
		}
		if err := wt.Reset(&gogit.ResetOptions{
			Mode:   gogit.HardReset,
			Commit: remoteRef.Hash(),
		}); err != nil {
			return fmt.Errorf("reset to origin/%s: %w", gad.Ref.Branch, err)
		}
		// Update the local branch ref to match (equivalent to
		// `git checkout <branch>` after the fetch+reset).
		localRef := plumbing.NewBranchReferenceName(gad.Ref.Branch)
		if err := repo.Storer.SetReference(plumbing.NewHashReference(localRef, remoteRef.Hash())); err != nil {
			return fmt.Errorf("update local branch %s: %w", gad.Ref.Branch, err)
		}
		// Point HEAD at the local branch.
		head := plumbing.NewSymbolicReference(plumbing.HEAD, localRef)
		if err := repo.Storer.SetReference(head); err != nil {
			return fmt.Errorf("update HEAD to %s: %w", gad.Ref.Branch, err)
		}
		return nil
	}

	if gad.Ref.Tag != "" {
		// `git fetch --tags --force origin` equivalent.
		fetchOpts := &gogit.FetchOptions{
			RemoteName: "origin",
			Auth:       auth,
			Force:      true,
			Tags:       gogit.AllTags,
			RefSpecs:   []gogitcfg.RefSpec{"+refs/tags/*:refs/tags/*"},
		}
		if err := repo.Fetch(fetchOpts); err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
			return fmt.Errorf("fetch tags: %w", sanitizeAuthErr(err))
		}
		return checkoutTag(repo, repoDir, gad.Ref.Tag, gad)
	}

	return fmt.Errorf("no branch or tag specified in ref config")
}

// checkoutTag resolves the configured tag selector ("semver", "any",
// or an exact tag name) against the repo's tag list and checks out
// the resulting tag.
func checkoutTag(repo *gogit.Repository, repoDir, tagRef string, _ *config.GitAutoDeployConfig) error {
	tags, err := listTags(repo)
	if err != nil {
		return fmt.Errorf("listing tags: %w", err)
	}

	var matchedTag string
	switch strings.ToLower(tagRef) {
	case "semver":
		matchedTag = findHighestSemverTag(tags)
		if matchedTag == "" {
			return fmt.Errorf("no valid semver tags found")
		}
	case "any":
		if len(tags) == 0 {
			return fmt.Errorf("no tags found")
		}
		matchedTag = tags[len(tags)-1]
	default:
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

	tagRefName := plumbing.NewTagReferenceName(matchedTag)
	tagRefObj, err := repo.Reference(tagRefName, true)
	if err != nil {
		return fmt.Errorf("resolve tag %s: %w", matchedTag, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	// Detached HEAD at the tag's commit. Equivalent to
	// `git checkout tags/<name>` which detaches HEAD on the commit
	// the tag points to.
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Hash:  tagRefObj.Hash(),
		Force: true,
	}); err != nil {
		return fmt.Errorf("checkout tag %s: %w", matchedTag, err)
	}
	return nil
}

// listTags returns every tag in the repo, in iteration order. The
// previous shell-out form (`git tag -l`) emits tags in lex order; the
// findHighestSemverTag pass below doesn't depend on input order, and
// "any" picks the last entry — match that behavior by sorting lex.
func listTags(repo *gogit.Repository) ([]string, error) {
	iter, err := repo.Tags()
	if err != nil {
		return nil, err
	}
	var tags []string
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		tags = append(tags, ref.Name().Short())
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(tags)
	return tags, nil
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

// setOriginURL replaces the origin remote with one pointing at repoURL.
// Tolerates "no origin yet" (fresh PlainOpen on a future-clone path).
func setOriginURL(repo *gogit.Repository, repoURL string) error {
	if _, err := repo.Remote("origin"); err == nil {
		if err := repo.DeleteRemote("origin"); err != nil {
			return fmt.Errorf("drop origin: %w", err)
		}
	}
	_, err := repo.CreateRemote(&gogitcfg.RemoteConfig{
		Name: "origin",
		URLs: []string{repoURL},
	})
	if err != nil {
		return fmt.Errorf("create origin: %w", err)
	}
	return nil
}

// buildAuth returns the go-git AuthMethod that should be passed to
// Clone / Fetch / Push. nil is returned when no creds are configured —
// the host's default behaviour applies (anonymous HTTPS / fall through
// to whatever git's helpers / SSH agent provides).
//
// Conventions match the previous shell-out form:
//   - GitHub PATs: "x-access-token" / PAT, OR username + PAT
//   - GitLab PATs: "oauth2" / PAT
//   - Bitbucket: username + app password
func buildAuth(creds *config.GitCredentials) (gogittransport.AuthMethod, error) {
	if creds == nil || (creds.Username == "" && creds.Password == "") {
		return nil, nil
	}
	switch {
	case creds.Username != "" && creds.Password != "":
		return &githttp.BasicAuth{Username: creds.Username, Password: creds.Password}, nil
	case creds.Password != "":
		// Token-only auth (common for GitHub).
		return &githttp.BasicAuth{Username: "x-access-token", Password: creds.Password}, nil
	default:
		// Username set but no password — almost certainly a misconfigured
		// `{{env:VAR}}` template that resolved to empty.
		return nil, fmt.Errorf("creds.username=%q is set but creds.password is empty (env var unset?)", creds.Username)
	}
}

// buildAuthURL is retained so the staging git verbs (which still need
// to embed creds in the URL when invoking shell-out git for `rebase`)
// can produce a usable URL. New callers should prefer buildAuth +
// passing the AuthMethod through go-git.
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
		u.User = url.UserPassword("x-access-token", creds.Password)
	default:
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

// sanitizeAuthErr scrubs URL-embedded credentials that can leak into
// go-git's wrapped errors (it includes the full URL in transport
// errors). Pattern: "://user:pass@" → "://***@", same shape as the
// previous shell-out's sanitizeGitOutput.
func sanitizeAuthErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if idx := strings.Index(msg, "://"); idx >= 0 {
		if atIdx := strings.Index(msg[idx:], "@"); atIdx >= 0 {
			msg = msg[:idx] + "://***" + msg[idx+atIdx:]
			return fmt.Errorf("%s", msg)
		}
	}
	return err
}

// runGit shells out to `git`. Retained for the single remaining call
// site (StagingPull's rebase — see staginggit.go) because go-git has
// no native rebase support. Every other git operation in this package
// is now go-git-native.
func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	output, err := cmd.CombinedOutput()
	if err != nil {
		sanitized := sanitizeGitOutput(string(output))
		return fmt.Errorf("git %s: %w: %s", strings.Join(args[:min(len(args), 2)], " "), err, sanitized)
	}
	return nil
}

// sanitizeGitOutput scrubs potential credentials from git output.
func sanitizeGitOutput(output string) string {
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

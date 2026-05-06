package sitedeploy

// Staging-side git verbs invoked by the operator via hulactl: stage,
// commit, push. They operate against StagingSrcDir of the named server
// (which by Stage 5b is the git working tree the long-lived staging
// container serves from). All three refuse cleanly when the server
// isn't `hula_build: staging` or when StagingSrcDir is not a git
// working tree.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
)

// ErrNotStaging is returned when a staging git verb is called against a
// server whose hula_build is not `staging`.
var ErrNotStaging = errors.New("server is not a staging server")

// ErrNotGitRepo is returned when StagingSrcDir is not a git working
// tree. Distinct from ErrNotStaging so the caller can produce a more
// informative HTTP status (400 vs 412).
var ErrNotGitRepo = errors.New("staging source dir is not a git working tree")

// StagingGitContext bundles the per-call resolved paths so the verbs
// don't have to re-derive them. Callers obtain one via
// NewStagingGitContext (which performs the staging+git checks).
type StagingGitContext struct {
	ServerID string
	WorkDir  string
	GAD      *config.GitAutoDeployConfig
}

// CommitterName returns the configured committer name, falling back to
// the hula-staging default when the operator hasn't set one.
func (c *StagingGitContext) CommitterName() string {
	if c.GAD != nil && c.GAD.Committer != nil && c.GAD.Committer.Name != "" {
		return c.GAD.Committer.Name
	}
	return "hula-staging"
}

// CommitterEmail returns the configured committer email, with the
// hula-staging fallback.
func (c *StagingGitContext) CommitterEmail() string {
	if c.GAD != nil && c.GAD.Committer != nil && c.GAD.Committer.Email != "" {
		return c.GAD.Committer.Email
	}
	return "staging@hula.local"
}

// NewStagingGitContext validates that s is a staging server with a git
// working tree at StagingSrcDir, returning a context the verbs use.
func NewStagingGitContext(s *config.Server) (*StagingGitContext, error) {
	if s == nil || s.GitAutoDeploy == nil {
		return nil, ErrNotStaging
	}
	if !isStagingProfile(s) {
		return nil, ErrNotStaging
	}
	if !IsGitWorkingTree(s.GitAutoDeploy.StagingSrcDir) {
		return nil, ErrNotGitRepo
	}
	return &StagingGitContext{
		ServerID: s.ID,
		WorkDir:  s.GitAutoDeploy.StagingSrcDir,
		GAD:      s.GitAutoDeploy,
	}, nil
}

// StagingStage runs `git add` against the named paths inside
// StagingSrcDir. Empty paths means "stage everything" (`git add -A`).
// Each path must remain inside StagingSrcDir — escapes (../) are
// rejected. Returns the list of paths actually staged.
func StagingStage(ctx *StagingGitContext, paths []string) ([]string, error) {
	if ctx == nil {
		return nil, fmt.Errorf("nil context")
	}

	if len(paths) == 0 {
		// `git add -A` stages additions, modifications, and deletions —
		// the most useful default for "I'm done editing, prepare a
		// commit." Operators wanting selective staging pass paths.
		if err := runGit(ctx.WorkDir, "add", "-A"); err != nil {
			return nil, fmt.Errorf("git add -A: %w", err)
		}
		return []string{"."}, nil
	}

	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		safe, err := sanitizeStagingPath(ctx.WorkDir, p)
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", p, err)
		}
		clean = append(clean, safe)
	}

	args := append([]string{"add", "--"}, clean...)
	if err := runGit(ctx.WorkDir, args...); err != nil {
		return nil, fmt.Errorf("git add: %w", err)
	}
	log.Infof("sitedeploy: staging %s: staged %d path(s)", ctx.ServerID, len(clean))
	return clean, nil
}

// StagingCommit runs `git commit -m <message>` and appends a
// "Committed-by: Hula" trailer line on its own line. Returns the new
// commit's short SHA on success. Refuses (with no error change to the
// tree) when there is nothing staged.
func StagingCommit(ctx *StagingGitContext, message, authorName, authorEmail string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("nil context")
	}
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("commit message is required")
	}

	// Quick bail-out when there's nothing to commit so we don't return
	// the ambiguous "git commit nothing to commit, working tree clean"
	// error from git.
	out, err := runGitOutput(ctx.WorkDir, "diff", "--cached", "--name-only")
	if err != nil {
		return "", fmt.Errorf("checking staged changes: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("nothing staged — run hulactl stage first")
	}

	body := strings.TrimRight(message, "\n") + "\n\nCommitted-by: Hula"

	args := []string{"commit", "-m", body}
	// Configure author identity inline so the commit is attributable
	// even on a fresh container with no `git config user.email`. The
	// caller (handler) supplies the operator's identity if it knows
	// one; otherwise we fall back to the configured committer
	// identity (root_git_autodeploy.committer.{name,email}), which
	// itself falls back to "hula-staging".
	name, email := authorName, authorEmail
	if name == "" {
		name = ctx.CommitterName()
	}
	if email == "" {
		email = ctx.CommitterEmail()
	}
	args = append([]string{
		"-c", "user.name=" + name,
		"-c", "user.email=" + email,
	}, args...)

	if err := runGit(ctx.WorkDir, args...); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}

	sha, err := runGitOutput(ctx.WorkDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	sha = strings.TrimSpace(sha)
	log.Infof("sitedeploy: staging %s: committed %s", ctx.ServerID, sha)
	return sha, nil
}

// StagingPullResult captures everything a pull attempt produced —
// success or failure. The rewind fields are populated when a conflict
// during rebase triggered a reset back to the pre-pull HEAD.
type StagingPullResult struct {
	SHA       string
	Branch    string
	Advanced  bool
	Rewound   bool
	RewoundTo string
}

// StagingPull updates the staging working tree from origin/<branch>
// using fetch + rebase. The rebase model means:
//
//   - Origin clean ahead → fast-forward, no merge commits.
//   - Origin diverged from local commits → local commits are replayed
//     on top of origin/<branch>. No merge commits clutter the history.
//   - Conflict during replay → we abort the rebase AND reset --hard
//     to the pre-pull HEAD. The served site (which reads through the
//     bind-mounted working tree) returns to its known-good pre-pull
//     state, and the result struct flags Rewound=true so the caller
//     surfaces what happened.
//
// A dirty working tree is still a hard refusal — pulling on top of
// uncommitted edits is too easy to lose work to silently.
//
// On the rewind path, the returned error is non-nil AND the result is
// non-nil. Callers (StagingSync in particular) inspect Rewound to
// adjust their own behaviour.
func StagingPull(ctx *StagingGitContext) (*StagingPullResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("nil context")
	}
	if ctx.GAD.Ref.Branch == "" {
		return nil, fmt.Errorf("only branch refs are pullable; ref.tag is read-only")
	}

	out, err := runGitOutput(ctx.WorkDir, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("checking working tree: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		return nil, fmt.Errorf("working tree has uncommitted changes — run hulactl stage + hulactl commit first, or wipe staging_src_dir to re-seed")
	}

	repoURL, err := buildAuthURL(ctx.GAD.Repo, ctx.GAD.Creds)
	if err != nil {
		return nil, fmt.Errorf("building repo URL: %w", err)
	}
	if err := runGit(ctx.WorkDir, "remote", "set-url", "origin", repoURL); err != nil {
		return nil, fmt.Errorf("git remote set-url: %w", err)
	}

	beforeSHA, _ := runGitOutput(ctx.WorkDir, "rev-parse", "HEAD")
	beforeSHA = strings.TrimSpace(beforeSHA)

	if err := runGit(ctx.WorkDir, "fetch", "origin", ctx.GAD.Ref.Branch); err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}

	// Rebase replays local commits on top of origin's tip. Clean
	// FF when local has nothing; clean linear history when local has
	// commits and there are no conflicts; aborts cleanly on conflict.
	//
	// Inline -c user.{name,email} so rebase can set the committer
	// identity when it rewrites local commits onto origin's tip. The
	// staging container has no `git config user.email` baked in, so
	// without these flags rebase fails with "empty ident name" the
	// moment there's anything to replay. Same identity StagingCommit
	// uses by default.
	if err := runGit(ctx.WorkDir,
		"-c", "user.name="+ctx.CommitterName(),
		"-c", "user.email="+ctx.CommitterEmail(),
		"rebase", "origin/"+ctx.GAD.Ref.Branch); err != nil {
		// Conflict (or any other rebase failure). Best-effort
		// cleanup: --abort handles the in-progress-rebase case;
		// reset --hard belts-and-braces in case --abort missed
		// something. Both errors are tolerated — what matters is
		// that we end up at beforeSHA with a clean tree.
		_ = runGit(ctx.WorkDir, "rebase", "--abort")
		rewound := false
		if beforeSHA != "" {
			if rwErr := runGit(ctx.WorkDir, "reset", "--hard", beforeSHA); rwErr == nil {
				rewound = true
				log.Warnf("sitedeploy: staging %s: rebase failed during pull — rewound to %s", ctx.ServerID, beforeSHA)
			} else {
				log.Errorf("sitedeploy: staging %s: rebase AND rewind both failed: rebase=%s rewind=%s", ctx.ServerID, err, rwErr)
			}
		}
		return &StagingPullResult{
				Branch:    ctx.GAD.Ref.Branch,
				Rewound:   rewound,
				RewoundTo: beforeSHA,
			}, fmt.Errorf("rebase origin/%s failed (likely conflict): %w", ctx.GAD.Ref.Branch, err)
	}

	afterSHA, err := runGitOutput(ctx.WorkDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	afterSHA = strings.TrimSpace(afterSHA)

	advanced := beforeSHA != ""
	if advanced {
		shortBefore := beforeSHA
		if len(shortBefore) > len(afterSHA) {
			shortBefore = shortBefore[:len(afterSHA)]
		}
		advanced = shortBefore != afterSHA
	}
	if advanced {
		log.Infof("sitedeploy: staging %s: pulled origin/%s — HEAD now %s", ctx.ServerID, ctx.GAD.Ref.Branch, afterSHA)
	} else {
		log.Infof("sitedeploy: staging %s: already up to date with origin/%s at %s", ctx.ServerID, ctx.GAD.Ref.Branch, afterSHA)
	}
	return &StagingPullResult{
		SHA:      afterSHA,
		Branch:   ctx.GAD.Ref.Branch,
		Advanced: advanced,
	}, nil
}

// StagingSync runs a fast-forward pull immediately followed by a push,
// in a single API call. The whole point is so the operator can issue
// one verb instead of remembering to pull-before-push.
//
// Failure semantics — site availability comes first:
//
//   - Pull fails (dirty tree, divergence): HEAD is unchanged. Return
//     the error verbatim so the operator knows what to fix.
//   - Pull succeeds, push fails: rewind. We reset --hard to the
//     pre-sync HEAD so the working tree (and therefore what the
//     staging container serves from the bind mount) returns to the
//     state the operator originally had. The operator's local edits
//     and commits stay intact at originalHEAD.
//
// The rewind is the part that distinguishes sync from a naive
// pull-then-push: a half-applied sync (pull OK, push fails) would
// otherwise leave the served site advanced beyond what the operator
// thought they shipped.
func StagingSync(ctx *StagingGitContext) (*StagingSyncResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("nil context")
	}

	originalHEAD, err := runGitOutput(ctx.WorkDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("recording pre-sync HEAD: %w", err)
	}
	originalHEAD = strings.TrimSpace(originalHEAD)

	pullRes, err := StagingPull(ctx)
	if err != nil {
		// Pull either refused (dirty tree) or rebased-then-rewound
		// (conflict). Either way, sync stops — push would fail
		// against the unchanged HEAD anyway. Surface the pull's
		// rewind info so the caller can render it.
		out := &StagingSyncResult{Branch: ctx.GAD.Ref.Branch}
		if pullRes != nil {
			out.PullSHA = pullRes.SHA
			out.Rewound = pullRes.Rewound
			out.RewoundTo = pullRes.RewoundTo
		}
		out.PushFailedErr = ""
		return out, fmt.Errorf("pull: %w", err)
	}

	pushSHA, err := StagingPush(ctx)
	if err != nil {
		// Push failed AFTER pull advanced HEAD. Rewind so the served
		// site reverts to its pre-sync state. The operator's commits
		// (if any) are still recoverable at originalHEAD.
		rewound := false
		if pullRes.Advanced && originalHEAD != "" {
			if rwErr := runGit(ctx.WorkDir, "reset", "--hard", originalHEAD); rwErr == nil {
				rewound = true
				log.Warnf("sitedeploy: staging %s: push failed after pull — rewound to %s", ctx.ServerID, originalHEAD)
			} else {
				log.Errorf("sitedeploy: staging %s: push failed AND rewind to %s also failed: %s", ctx.ServerID, originalHEAD, rwErr)
			}
		}
		return &StagingSyncResult{
			Branch:        ctx.GAD.Ref.Branch,
			PullSHA:       pullRes.SHA,
			Pulled:        pullRes.Advanced,
			Rewound:       rewound,
			RewoundTo:     originalHEAD,
			PushFailedErr: err.Error(),
		}, fmt.Errorf("push: %w", err)
	}

	log.Infof("sitedeploy: staging %s: sync OK — pulled=%t pushed=%s", ctx.ServerID, pullRes.Advanced, pushSHA)
	return &StagingSyncResult{
		Branch:  ctx.GAD.Ref.Branch,
		PullSHA: pullRes.SHA,
		Pulled:  pullRes.Advanced,
		PushSHA: pushSHA,
	}, nil
}

// StagingSyncResult is the result of a sync. The success path leaves
// PushFailedErr empty; the rewind path populates RewoundTo with the SHA
// the working tree reverted to.
type StagingSyncResult struct {
	Branch        string
	PullSHA       string
	Pulled        bool
	PushSHA       string
	Rewound       bool
	RewoundTo     string
	PushFailedErr string
}

// StagingPush pushes the current branch to origin. The push uses the
// ref configured in root_git_autodeploy (branch only — pushing tags is
// out of scope here). Auth credentials come from gad.Creds, the same
// way CloneOrPull uses them.
func StagingPush(ctx *StagingGitContext) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("nil context")
	}
	if ctx.GAD.Ref.Branch == "" {
		return "", fmt.Errorf("only branch refs are pushable; ref.tag is read-only")
	}

	repoURL, err := buildAuthURL(ctx.GAD.Repo, ctx.GAD.Creds)
	if err != nil {
		return "", fmt.Errorf("building repo URL: %w", err)
	}

	// Refresh origin so new credentials take effect even if they
	// rotated since clone time. Not destructive — same URL with
	// possibly-different embedded creds.
	if err := runGit(ctx.WorkDir, "remote", "set-url", "origin", repoURL); err != nil {
		return "", fmt.Errorf("git remote set-url: %w", err)
	}

	if err := runGit(ctx.WorkDir, "push", "origin", "HEAD:"+ctx.GAD.Ref.Branch); err != nil {
		return "", fmt.Errorf("git push: %w", err)
	}

	sha, _ := runGitOutput(ctx.WorkDir, "rev-parse", "--short", "HEAD")
	sha = strings.TrimSpace(sha)
	log.Infof("sitedeploy: staging %s: pushed %s to origin/%s", ctx.ServerID, sha, ctx.GAD.Ref.Branch)
	return sha, nil
}

// sanitizeStagingPath rejects paths that escape workDir (e.g. ../,
// absolute paths pointing outside the tree, or null bytes). Returns the
// path normalized to a workDir-relative form so it works cleanly with
// `git add` (which always interprets paths relative to its CWD).
func sanitizeStagingPath(workDir, p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("null byte in path")
	}
	cleaned := filepath.Clean(p)
	if filepath.IsAbs(cleaned) {
		// Allow absolute paths that resolve inside workDir; rewrite
		// them as relative.
		rel, err := filepath.Rel(workDir, cleaned)
		if err != nil {
			return "", err
		}
		cleaned = rel
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes staging dir")
	}
	return cleaned, nil
}

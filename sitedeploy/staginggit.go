package sitedeploy

// Staging-side git verbs invoked by the operator via hulactl: stage,
// commit, push, pull, sync. They operate against StagingSrcDir of the
// named server (which by Stage 5b is the git working tree the long-
// lived staging container serves from). All refuse cleanly when the
// server isn't `hula_build: staging` or when StagingSrcDir is not a
// git working tree.
//
// All operations except the rebase inside StagingPull are go-git-
// native. Rebase has no go-git equivalent (long-standing upstream
// gap) so it remains a `git rebase` shell-out on a single, well-
// scoped path.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

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
	wt, err := openWorktree(ctx.WorkDir)
	if err != nil {
		return nil, err
	}

	if len(paths) == 0 {
		// AddOptions{All: true} == `git add -A` — stages additions,
		// modifications, and deletions in one call.
		if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
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
	for _, p := range clean {
		if _, err := wt.Add(p); err != nil {
			return nil, fmt.Errorf("git add %s: %w", p, err)
		}
	}
	log.Infof("sitedeploy: staging %s: staged %d path(s)", ctx.ServerID, len(clean))
	return clean, nil
}

// StagingCommit creates a commit with `<message>\n\nCommitted-by: Hula`
// as the body, signed with the operator-supplied (or fallback)
// identity. Returns the new commit's short SHA on success. Refuses
// (with no error change to the tree) when there is nothing staged.
func StagingCommit(ctx *StagingGitContext, message, authorName, authorEmail string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("nil context")
	}
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("commit message is required")
	}

	repo, wt, err := openRepoAndWorktree(ctx.WorkDir)
	if err != nil {
		return "", err
	}

	// Quick bail-out when there's nothing to commit so we don't return
	// the ambiguous "nothing to commit" error from go-git's commit
	// path. Status(.Staged()) returns the set of files with index
	// changes; if empty, we have nothing to commit.
	status, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("checking staged changes: %w", err)
	}
	staged := false
	for _, fs := range status {
		if fs.Staging != gogit.Unmodified && fs.Staging != gogit.Untracked {
			staged = true
			break
		}
	}
	if !staged {
		return "", fmt.Errorf("nothing staged — run hulactl stage first")
	}

	body := strings.TrimRight(message, "\n") + "\n\nCommitted-by: Hula"

	name, email := authorName, authorEmail
	if name == "" {
		name = ctx.CommitterName()
	}
	if email == "" {
		email = ctx.CommitterEmail()
	}
	now := time.Now()
	sig := &object.Signature{Name: name, Email: email, When: now}

	hash, err := wt.Commit(body, &gogit.CommitOptions{
		Author:    sig,
		Committer: sig,
	})
	if err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}

	short := shortSHA(repo, hash)
	log.Infof("sitedeploy: staging %s: committed %s", ctx.ServerID, short)
	return short, nil
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
//
// The fetch + status + reset steps are go-git-native; the rebase
// itself shells out to `git rebase` because go-git has no native
// rebase. This is the only shell-out left in the staging git verb
// surface.
func StagingPull(ctx *StagingGitContext) (*StagingPullResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("nil context")
	}
	if ctx.GAD.Ref.Branch == "" {
		return nil, fmt.Errorf("only branch refs are pullable; ref.tag is read-only")
	}

	repo, wt, err := openRepoAndWorktree(ctx.WorkDir)
	if err != nil {
		return nil, err
	}

	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("checking working tree: %w", err)
	}
	if !status.IsClean() {
		return nil, fmt.Errorf("working tree has uncommitted changes — run hulactl stage + hulactl commit first, or wipe staging_src_dir to re-seed")
	}

	if err := setOriginURL(repo, ctx.GAD.Repo); err != nil {
		return nil, fmt.Errorf("git remote set-url: %w", err)
	}

	beforeRef, _ := repo.Head()
	var beforeSHA string
	if beforeRef != nil {
		beforeSHA = beforeRef.Hash().String()
	}

	auth, err := buildAuth(ctx.GAD.Creds)
	if err != nil {
		return nil, fmt.Errorf("building auth: %w", err)
	}
	branch := ctx.GAD.Ref.Branch
	refSpec := gogitcfg.RefSpec(fmt.Sprintf(
		"+refs/heads/%s:refs/remotes/origin/%s", branch, branch,
	))
	if err := repo.Fetch(&gogit.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Force:      true,
		RefSpecs:   []gogitcfg.RefSpec{refSpec},
	}); err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return nil, fmt.Errorf("git fetch: %w", sanitizeAuthErr(err))
	}

	// Rebase replays local commits on top of origin's tip. Clean FF
	// when local has nothing; clean linear history when local has
	// commits and there are no conflicts; aborts cleanly on conflict.
	//
	// SHELL-OUT NOTE: go-git has no native rebase. Wrapping `git
	// rebase` is the pragmatic alternative — same operation, one
	// well-isolated boundary. The buildAuthURL call below is also
	// retained for that reason: the rebase shell-out can't take a
	// go-git AuthMethod, so we embed creds in the origin URL just
	// for that one invocation. setOriginURL above already pointed
	// origin at the credentialed URL via buildAuthURL behavior in
	// the parent flow; explicitly call it here too to keep it
	// fresh in case auth rotated since clone time.
	credURL, err := buildAuthURL(ctx.GAD.Repo, ctx.GAD.Creds)
	if err != nil {
		return nil, fmt.Errorf("building auth url for rebase shell-out: %w", err)
	}
	if err := runGit(ctx.WorkDir, "remote", "set-url", "origin", credURL); err != nil {
		return nil, fmt.Errorf("git remote set-url: %w", err)
	}
	if err := runGit(ctx.WorkDir,
		"-c", "user.name="+ctx.CommitterName(),
		"-c", "user.email="+ctx.CommitterEmail(),
		"rebase", "origin/"+branch); err != nil {
		// Conflict (or any other rebase failure). Best-effort
		// cleanup: --abort handles the in-progress-rebase case;
		// reset --hard belts-and-braces in case --abort missed
		// something. Both errors are tolerated — what matters is
		// that we end up at beforeSHA with a clean tree.
		_ = runGit(ctx.WorkDir, "rebase", "--abort")
		rewound := false
		if beforeSHA != "" {
			if rwErr := wt.Reset(&gogit.ResetOptions{
				Mode:   gogit.HardReset,
				Commit: plumbing.NewHash(beforeSHA),
			}); rwErr == nil {
				rewound = true
				log.Warnf("sitedeploy: staging %s: rebase failed during pull — rewound to %s", ctx.ServerID, beforeSHA)
			} else {
				log.Errorf("sitedeploy: staging %s: rebase AND rewind both failed: rebase=%s rewind=%s", ctx.ServerID, err, rwErr)
			}
		}
		return &StagingPullResult{
				Branch:    branch,
				Rewound:   rewound,
				RewoundTo: beforeSHA,
			}, fmt.Errorf("rebase origin/%s failed (likely conflict): %w", branch, err)
	}

	afterRef, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	afterShort := shortSHA(repo, afterRef.Hash())
	advanced := beforeSHA != "" && beforeSHA != afterRef.Hash().String()
	if advanced {
		log.Infof("sitedeploy: staging %s: pulled origin/%s — HEAD now %s", ctx.ServerID, branch, afterShort)
	} else {
		log.Infof("sitedeploy: staging %s: already up to date with origin/%s at %s", ctx.ServerID, branch, afterShort)
	}
	return &StagingPullResult{
		SHA:      afterShort,
		Branch:   branch,
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

	repo, wt, err := openRepoAndWorktree(ctx.WorkDir)
	if err != nil {
		return nil, err
	}
	originalRef, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("recording pre-sync HEAD: %w", err)
	}
	originalSHA := originalRef.Hash().String()

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
		// (if any) are still recoverable at originalSHA.
		rewound := false
		if pullRes.Advanced && originalSHA != "" {
			if rwErr := wt.Reset(&gogit.ResetOptions{
				Mode:   gogit.HardReset,
				Commit: plumbing.NewHash(originalSHA),
			}); rwErr == nil {
				rewound = true
				log.Warnf("sitedeploy: staging %s: push failed after pull — rewound to %s", ctx.ServerID, originalSHA)
			} else {
				log.Errorf("sitedeploy: staging %s: push failed AND rewind to %s also failed: %s", ctx.ServerID, originalSHA, rwErr)
			}
		}
		return &StagingSyncResult{
			Branch:        ctx.GAD.Ref.Branch,
			PullSHA:       pullRes.SHA,
			Pulled:        pullRes.Advanced,
			Rewound:       rewound,
			RewoundTo:     originalSHA,
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

	repo, err := gogit.PlainOpen(ctx.WorkDir)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}
	if err := setOriginURL(repo, ctx.GAD.Repo); err != nil {
		return "", fmt.Errorf("git remote set-url: %w", err)
	}
	auth, err := buildAuth(ctx.GAD.Creds)
	if err != nil {
		return "", fmt.Errorf("building auth: %w", err)
	}
	branch := ctx.GAD.Ref.Branch
	if err := repo.Push(&gogit.PushOptions{
		RemoteName: "origin",
		Auth:       auth,
		RefSpecs: []gogitcfg.RefSpec{
			gogitcfg.RefSpec(fmt.Sprintf("HEAD:refs/heads/%s", branch)),
		},
	}); err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return "", fmt.Errorf("git push: %w", sanitizeAuthErr(err))
	}

	headRef, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	short := shortSHA(repo, headRef.Hash())
	log.Infof("sitedeploy: staging %s: pushed %s to origin/%s", ctx.ServerID, short, branch)
	return short, nil
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

// openWorktree is the common path: open the repo, grab its worktree.
// Returns a wrapped error so callers can pass it up unchanged.
func openWorktree(dir string) (*gogit.Worktree, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	return wt, nil
}

// openRepoAndWorktree returns both for the (common) case where a verb
// needs Repository-level ops AND worktree-level ops.
func openRepoAndWorktree(dir string) (*gogit.Repository, *gogit.Worktree, error) {
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("open repo: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, nil, fmt.Errorf("worktree: %w", err)
	}
	return repo, wt, nil
}

// shortSHA returns the 7-char abbreviated form go's git tooling and
// the previous shell-out flow both emit. go-git doesn't have a
// disambiguating short-sha helper (CommitObject + Object.ShortHash
// would do better), but a fixed 7-char prefix matches the operator-
// visible logs and JSON responses that hulactl already prints.
func shortSHA(_ *gogit.Repository, hash plumbing.Hash) string {
	full := hash.String()
	if len(full) >= 7 {
		return full[:7]
	}
	return full
}

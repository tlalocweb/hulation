// Package emailverify wraps github.com/AfterShip/email-verifier
// for the Phase-4b /chat/start endpoint. The library does syntax
// + DNS + disposable + role + misspell checks fully offline; we
// keep SMTP probing OFF by default because outbound :25 is
// frequently blocked or greylisted, which blows up p99 on the
// /chat/start path.
//
// The verifier is expensive to construct (the disposable-domain
// list is loaded from an embedded blob on first use), so we hold
// a process-singleton built from the operator's chat config at
// boot.
package emailverify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	emailverifier "github.com/AfterShip/email-verifier"
)

// ErrInvalid is returned when the address fails any of the enabled
// checks. Wrap it with the specific reason for log/UI surfacing
// (we surface the AfterShip reason verbatim on /chat/start so the
// widget can render "did you mean gmail.com?").
var ErrInvalid = errors.New("emailverify: invalid")

// Result is the small subset of AfterShip's Result we surface.
// The full struct has 20+ fields; the SPA / widget only need the
// reason classification.
type Result struct {
	Email      string
	Reason     string // machine-readable: "syntax" | "dns" | "disposable" | "role_account" | "misspell" | "smtp"
	Suggestion string // populated when MisspellCheck is on and a typo is detected
}

// Options control which AfterShip checks are enabled. Mirrors
// config.ChatEmailVerifierConfig but as plain bools so this
// package stays independent of the config layer.
type Options struct {
	SMTPCheck       bool
	DisposableCheck bool
	RoleCheck       bool
	MisspellCheck   bool
	// DNSCheck requires the domain to publish MX records. On by default
	// (it's the cheapest real signal that an address could receive mail),
	// but it is the one check that needs working outbound DNS — so a
	// sandboxed or offline environment must be able to turn it off, the
	// same way SMTPCheck is opt-in for its network dependency.
	DNSCheck bool
}

// DefaultOptions matches the documented production defaults from
// PLAN_4B.md §5.3: SMTP off; disposable + role + misspell + DNS on.
func DefaultOptions() Options {
	return Options{
		SMTPCheck:       false,
		DisposableCheck: true,
		RoleCheck:       true,
		MisspellCheck:   true,
		DNSCheck:        true,
	}
}

// Verifier is the package-internal handle. Construct with New().
type Verifier struct {
	v    *emailverifier.Verifier
	opts Options
}

// New builds a Verifier with the given options. Returns nil only
// when constructing the underlying library fails — which the
// AfterShip impl never actually does for the supported options.
func New(opts Options) *Verifier {
	v := emailverifier.NewVerifier()
	if opts.DisposableCheck {
		v = v.EnableAutoUpdateDisposable()
	}
	// AfterShip's library defaults are: syntax always on, role
	// check enabled, misspell on, smtp off. Configure to match
	// our explicit options.
	if !opts.SMTPCheck {
		v = v.DisableSMTPCheck()
	}
	return &Verifier{v: v, opts: opts}
}

// Verify runs the configured checks. ctx bounds the DNS lookups +
// optional SMTP probe (DNS resolves are quick; SMTP can take 30s
// against a slow upstream).
func (s *Verifier) Verify(ctx context.Context, email string) (Result, error) {
	if s == nil || s.v == nil {
		return Result{}, fmt.Errorf("emailverify: verifier not initialised")
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return Result{Email: email, Reason: "syntax"}, fmt.Errorf("%w: empty email", ErrInvalid)
	}
	// With DNS disabled we must not call the library's Verify at all: it
	// performs an MX lookup unconditionally and returns a hard error when
	// resolution fails, so the failure lands before any per-check gate could
	// consider it. Run the offline primitives directly instead — they are the
	// same ones Verify composes, minus the network.
	if !s.opts.DNSCheck {
		return s.verifyOffline(email)
	}
	// AfterShip's API doesn't take a Context. Run in a goroutine
	// and fight ctx ourselves so the caller can apply a timeout.
	type out struct {
		r   *emailverifier.Result
		err error
	}
	ch := make(chan out, 1)
	go func() {
		r, err := s.v.Verify(email)
		ch <- out{r: r, err: err}
	}()
	select {
	case <-ctx.Done():
		return Result{Email: email}, fmt.Errorf("emailverify: %w", ctx.Err())
	case o := <-ch:
		if o.err != nil {
			return Result{Email: email, Reason: "syntax"},
				fmt.Errorf("%w: %v", ErrInvalid, o.err)
		}
		r := o.r
		if r == nil {
			return Result{Email: email, Reason: "syntax"},
				fmt.Errorf("%w: nil result", ErrInvalid)
		}
		// Misspell check — done client-side; no upstream call.
		if s.opts.MisspellCheck && r.Suggestion != "" {
			return Result{Email: email, Reason: "misspell", Suggestion: r.Suggestion},
				fmt.Errorf("%w: suggested %s", ErrInvalid, r.Suggestion)
		}
		if !r.Syntax.Valid {
			return Result{Email: email, Reason: "syntax"},
				fmt.Errorf("%w: bad syntax", ErrInvalid)
		}
		if r.Disposable && s.opts.DisposableCheck {
			return Result{Email: email, Reason: "disposable"},
				fmt.Errorf("%w: disposable domain", ErrInvalid)
		}
		if r.RoleAccount && s.opts.RoleCheck {
			return Result{Email: email, Reason: "role_account"},
				fmt.Errorf("%w: role-account address", ErrInvalid)
		}
		if !r.HasMxRecords {
			return Result{Email: email, Reason: "dns"},
				fmt.Errorf("%w: no MX records", ErrInvalid)
		}
		// SMTP probing only runs when enabled; we look at the SMTP
		// sub-result the library populated.
		if s.opts.SMTPCheck && r.SMTP != nil && !r.SMTP.Deliverable {
			return Result{Email: email, Reason: "smtp"},
				fmt.Errorf("%w: SMTP probe failed", ErrInvalid)
		}
		return Result{Email: email, Reason: "ok"}, nil
	}
}

// verifyOffline runs every check that needs no network: syntax, misspell
// suggestion, disposable-domain and role-account lookups. Used when DNSCheck
// is disabled.
//
// It deliberately mirrors the order of the networked path above so the same
// input yields the same Reason, and only the MX step is missing. SMTPCheck is
// ignored here — an SMTP probe without DNS is not possible, and DNSCheck=false
// already declares this environment can't resolve mail domains.
func (s *Verifier) verifyOffline(email string) (Result, error) {
	syn := s.v.ParseAddress(email)
	if !syn.Valid {
		return Result{Email: email, Reason: "syntax"},
			fmt.Errorf("%w: bad syntax", ErrInvalid)
	}
	if s.opts.MisspellCheck {
		if sug := s.v.SuggestDomain(syn.Domain); sug != "" && sug != syn.Domain {
			return Result{Email: email, Reason: "misspell", Suggestion: sug},
				fmt.Errorf("%w: suggested %s", ErrInvalid, sug)
		}
	}
	if s.opts.DisposableCheck && s.v.IsDisposable(syn.Domain) {
		return Result{Email: email, Reason: "disposable"},
			fmt.Errorf("%w: disposable domain", ErrInvalid)
	}
	if s.opts.RoleCheck && s.v.IsRoleAccount(syn.Username) {
		return Result{Email: email, Reason: "role_account"},
			fmt.Errorf("%w: role-account address", ErrInvalid)
	}
	return Result{Email: email, Reason: "ok"}, nil
}

// Singleton ----------------------------------------------------

var (
	muSingleton sync.RWMutex
	singleton   *Verifier
)

// SetSingleton installs the global Verifier the chat-start handler
// reads. Called once at boot from server/chat_boot.go after the
// config is loaded.
func SetSingleton(v *Verifier) {
	muSingleton.Lock()
	defer muSingleton.Unlock()
	singleton = v
}

// Singleton returns the installed verifier or nil when /chat is
// disabled / pre-boot. Callers should treat nil as "service not
// ready" and fail the request with 503.
func Singleton() *Verifier {
	muSingleton.RLock()
	defer muSingleton.RUnlock()
	return singleton
}

// SingletonTimeout is the default per-Verify deadline the
// /chat/start handler applies.
const SingletonTimeout = 5 * time.Second

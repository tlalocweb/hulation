package chat

// PushHook — fan-out for incoming-chat APNs/FCM notifications.
//
// When a visitor opens a new chat via POST /api/v1/chat/start, the
// handler invokes FireNewChatPush which:
//   1. Builds a notifier.Envelope with the visitor-facing alert text
//      (the iOS lock-screen card design: title=visitor email/id,
//      subtitle="New chat · Country", body=first message preview).
//   2. Attaches a deep-link payload under CustomData["hula"] that the
//      mobile apps parse to navigate straight to the thread on tap
//      (see hula-mobile/ios/Hula/PushDeepLink.swift +
//      hula-mobile/android/.../PushDeepLink.kt).
//   3. Resolves every push-enabled user's active devices, decrypts
//      their tokens via tokenbox.Open, and adds them as DeviceAddrs.
//   4. Hands the envelope to notifier.Global().Deliver() on a
//      goroutine so the /chat/start response isn't held up.
//
// Phase 5b scope: fan out to every user with PushEnabled=true. A
// per-server ACL refinement and a dedicated `chat_new` pref toggle
// land in a follow-up — for v0 we reuse the same PushEnabled flag
// that gates alert push.

import (
	"context"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/mobile/tokenbox"
	"github.com/tlalocweb/hulation/pkg/notifier"
	hulabolt "github.com/tlalocweb/hulation/pkg/store/bolt"
	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// pushTokenKey is the process-wide AES master key the tokenbox uses
// to seal/open device push tokens. Set at boot via SetPushTokenKey;
// nil disables push fan-out (the envelope still builds for testing).
var pushTokenKey []byte

// SetPushTokenKey wires the master key in. Mirrors the parallel call
// in pkg/alerts/evaluator.SetTokenKey — kept as separate state so the
// chat hook can be exercised independently in tests.
func SetPushTokenKey(k []byte) { pushTokenKey = k }

// ChatPushInput is the minimum set of fields the hook needs from the
// /chat/start handler. Kept as a flat struct so the handler doesn't
// have to construct a full Session-shaped value.
type ChatPushInput struct {
	SessionID      string
	ServerID       string
	VisitorID      string
	VisitorEmail   string
	VisitorCountry string
	FirstMessage   string
}

// FireNewChatPush builds the envelope, resolves recipients, and
// delivers via the global notifier composite. Designed to run on its
// own goroutine — never blocks the caller's response path and
// swallows errors after logging them.
func FireNewChatPush(ctx context.Context, in ChatPushInput) {
	env := BuildNewChatEnvelope(in)
	env.Recipients = resolveChatRecipients(ctx)
	if len(env.Recipients) == 0 {
		// Nothing to do — log at debug only; not interesting in prod.
		log.Debugf("chat push: no recipients for server %s session %s", in.ServerID, in.SessionID)
		return
	}
	n := notifier.Global()
	if n == nil {
		// notifier.Global is documented as nil-when-unset (e.g. tests
		// or installs without APNs/FCM configured). Guard the goroutine
		// against a nil-deref crash.
		log.Warnf("chat push: notifier.Global() is nil; skipping delivery for session %s", in.SessionID)
		return
	}
	rep, err := n.Deliver(ctx, env)
	if err != nil {
		log.Warnf("chat push: deliver: %v", err)
		return
	}
	if !rep.AnyOK() {
		log.Warnf("chat push: no channel delivered (session=%s recipients=%d)",
			in.SessionID, len(env.Recipients))
	}
}

// BuildNewChatEnvelope assembles the envelope without resolving
// recipients — separated for unit-testability and so future callers
// (web push, in-app banner via WS, etc.) can reuse the payload shape.
func BuildNewChatEnvelope(in ChatPushInput) notifier.Envelope {
	title := in.VisitorEmail
	if title == "" {
		title = in.VisitorID
	}
	if title == "" {
		title = "Anonymous visitor"
	}
	country := in.VisitorCountry
	if country == "" {
		country = "Unknown"
	}
	// Truncate by rune count, not bytes — byte slicing can split a
	// multi-byte UTF-8 rune (emoji, accented chars) and produce an
	// invalid push payload body.
	preview := in.FirstMessage
	if runes := []rune(preview); len(runes) > 180 {
		preview = string(runes[:177]) + "…"
	}

	return notifier.Envelope{
		ID:        uuid.NewString(),
		Subject:   title,
		ShortText: preview,
		// HTML body left empty — email backends ignore chat push.
		// A future "chat digest" email would fill this in.
		HTMLBody: "",
		// Chat-new pushes chime by default; alert pushes don't unless
		// they explicitly opt in. See notifier.Envelope.Sound docs.
		Sound: "default",
		CustomData: map[string]any{
			"hula": map[string]any{
				"kind":            "chat.new",
				"server_id":       in.ServerID,
				"session_id":      in.SessionID,
				"visitor_email":   in.VisitorEmail,
				"visitor_country": country,
				"subtitle":        "New chat · " + country,
			},
		},
	}
}

func resolveChatRecipients(ctx context.Context) []notifier.DeviceAddr {
	if pushTokenKey == nil {
		return nil
	}
	s := storage.Global()
	if s == nil {
		return nil
	}
	allPrefs, err := hulabolt.ListNotificationPrefs(ctx, s)
	if err != nil {
		log.Warnf("chat push: list prefs: %v", err)
		return nil
	}
	var out []notifier.DeviceAddr
	for _, p := range allPrefs {
		if !p.PushEnabled {
			continue
		}
		devs, err := hulabolt.ListDevicesForUser(ctx, s, p.UserID)
		if err != nil {
			continue
		}
		for _, d := range devs {
			if !d.Active || len(d.TokenCipher) == 0 {
				continue
			}
			plain, err := tokenbox.Open(d.TokenCipher, pushTokenKey)
			if err != nil {
				continue
			}
			ch := notifier.ChannelAPNS
			if d.Platform == "fcm" {
				ch = notifier.ChannelFCM
			}
			out = append(out, notifier.DeviceAddr{
				Channel:   ch,
				UserID:    d.UserID,
				DeviceID:  d.ID,
				PushToken: plain,
			})
		}
	}
	return out
}

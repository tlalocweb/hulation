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
//   3. Resolves every push-enabled user's active devices and splits them
//      into a "relay" cohort (devices with a registered relay channel +
//      X25519 encryption pub) and a "legacy" cohort (everyone else).
//   4. Relay cohort: seals the preview JSON to each device's X25519 pub
//      via pkg/push/preview, then POSTs the ciphertext through the relay
//      (pkg/push/relayclient) — one signed request per device.
//   5. Legacy cohort: decrypts the TokenCipher and falls through to
//      notifier.Global().Deliver() as before.
//
// Phase 5b scope: fan out to every user with PushEnabled=true. A
// per-server ACL refinement and a dedicated `chat_new` pref toggle
// land in a follow-up — for v0 we reuse the same PushEnabled flag
// that gates alert push.

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/tlalocweb/hulation/log"
	"github.com/tlalocweb/hulation/pkg/mobile/tokenbox"
	"github.com/tlalocweb/hulation/pkg/notifier"
	"github.com/tlalocweb/hulation/pkg/push/preview"
	"github.com/tlalocweb/hulation/pkg/push/relayclient"
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

// relayPushClient is the process-wide hula-push-relay client. Set at boot via
// SetRelayPushClient when the operator has configured a relay; nil disables the
// relay fan-out path so devices without RelayChannelID still get pushes through
// the legacy notifier route.
var relayPushClient *relayclient.Client

// SetRelayPushClient wires the relay client in. Pass nil to disable; pass a
// constructed client to enable the v1 sealed-preview fan-out path.
func SetRelayPushClient(c *relayclient.Client) { relayPushClient = c }

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

// FireNewChatPush builds the envelope, resolves recipients, and dispatches them on
// two paths:
//
//   - Relay cohort: devices that registered a relay channel + Noise encryption pub
//     (the v1 default). The preview JSON is sealed to the device's X25519 public key
//     and POSTed to the relay's /v1/push/send endpoint. Permanent failures
//     (channel_revoked) deactivate the device record; transient failures are logged
//     but don't break the rest of the fan-out.
//   - Legacy cohort: devices without those fields. Falls through to
//     notifier.Global().Deliver() — same path Phase 5b shipped with.
//
// Designed to run on its own goroutine: never blocks the caller's response path,
// errors only land in the log.
func FireNewChatPush(ctx context.Context, in ChatPushInput) {
	env := BuildNewChatEnvelope(in)
	relayCohort, legacyCohort := resolveChatRecipientCohorts(ctx)
	if len(relayCohort) == 0 && len(legacyCohort) == 0 {
		log.Debugf("chat push: no recipients for server %s session %s", in.ServerID, in.SessionID)
		return
	}

	// Relay path — seal + POST per device. Each send is independent: one bad
	// device doesn't poison the others.
	if len(relayCohort) > 0 {
		if relayPushClient == nil {
			log.Warnf(
				"chat push: %d device(s) have relay channels but no relay client configured (session=%s)",
				len(relayCohort), in.SessionID,
			)
		} else {
			previewBytes, err := json.Marshal(buildNewChatPreview(in))
			if err != nil {
				log.Warnf("chat push: marshal preview: %v", err)
			} else {
				for _, dev := range relayCohort {
					sendRelayChatPush(ctx, dev, previewBytes, in.SessionID)
				}
			}
		}
	}

	// Legacy path — existing notifier composite, unchanged.
	if len(legacyCohort) > 0 {
		env.Recipients = legacyCohort
		if n := notifier.Global(); n != nil {
			rep, err := n.Deliver(ctx, env)
			if err != nil {
				log.Warnf("chat push: deliver: %v", err)
			} else if !rep.AnyOK() {
				log.Warnf("chat push: no legacy channel delivered (session=%s recipients=%d)",
					in.SessionID, len(env.Recipients))
			}
		} else {
			log.Warnf("chat push: notifier.Global() is nil; skipping legacy delivery for session %s",
				in.SessionID)
		}
	}
}

// newChatPreviewPayload is the on-device JSON shape the mobile NSE / FBMS renders.
// Kept as a separate struct (rather than the notifier.Envelope CustomData blob) so
// the sealed plaintext is small — every byte costs APNs payload budget.
type newChatPreviewPayload struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Body     string `json:"body"`
	Kind     string `json:"kind"`
	SrvID    string `json:"server_id"`
	SesID    string `json:"session_id"`
}

func buildNewChatPreview(in ChatPushInput) newChatPreviewPayload {
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
	body := in.FirstMessage
	if runes := []rune(body); len(runes) > 180 {
		body = string(runes[:177]) + "…"
	}
	return newChatPreviewPayload{
		Title:    title,
		Subtitle: "New chat · " + country,
		Body:     body,
		Kind:     "chat.new",
		SrvID:    in.ServerID,
		SesID:    in.SessionID,
	}
}

// sendRelayChatPush seals the preview to one device's X25519 pub and POSTs it
// through the relay. Side effects: deactivates the device row when the relay
// reports channel_revoked (device dropped the token, switched apps, etc.).
func sendRelayChatPush(
	ctx context.Context,
	dev relayDevice,
	previewBytes []byte,
	sessionID string,
) {
	envelope, err := preview.Seal(nil, dev.encryptionPub, previewBytes)
	if err != nil {
		log.Warnf("chat push: seal for device %s: %v", dev.deviceID, err)
		return
	}
	ciphertextB64 := base64.RawURLEncoding.EncodeToString(envelope)
	outcome, _, err := relayPushClient.SendPush(ctx, relayclient.SendPushParams{
		PushChannelID: dev.channelID,
		ChannelAuth:   dev.channelAuth,
		Ciphertext:    ciphertextB64,
		CollapseID:    "hula-chat-" + sessionID,
		TTLSeconds:    24 * 60 * 60,
	})
	switch outcome {
	case relayclient.OutcomeDispatched:
		// Common path — log at debug to avoid spamming chat-heavy installs.
		log.Debugf("chat push (relay): dispatched device=%s session=%s", dev.deviceID, sessionID)
	case relayclient.OutcomeChannelRevoked:
		log.Infof(
			"chat push (relay): channel revoked, deactivating device=%s session=%s err=%v",
			dev.deviceID, sessionID, err,
		)
		deactivateDevice(ctx, dev.deviceID)
	case relayclient.OutcomeBadRequest:
		// Permanent operator-side bug — surface loudly so the fix lands fast.
		log.Warnf("chat push (relay): bad request device=%s session=%s err=%v",
			dev.deviceID, sessionID, err)
	default:
		// OutcomeRetryable: log warn; chat-push is fire-and-forget so we don't
		// retry from here. The mobile catches up via the gRPC chat stream when
		// the user opens the app anyway.
		log.Warnf("chat push (relay): %v outcome device=%s session=%s err=%v",
			outcome, dev.deviceID, sessionID, err)
	}
}

func deactivateDevice(ctx context.Context, deviceID string) {
	s := storage.Global()
	if s == nil {
		return
	}
	d, err := hulabolt.GetDevice(ctx, s, deviceID)
	if err != nil || d == nil {
		log.Warnf("chat push: deactivate: get device %s: %v", deviceID, err)
		return
	}
	d.Active = false
	if _, err := hulabolt.PutDevice(ctx, s, *d); err != nil {
		log.Warnf("chat push: deactivate: put device %s: %v", deviceID, err)
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

// relayDevice is the minimal set of fields the sealed-preview path needs from a
// `StoredDevice` row, with the channel auth already unsealed via tokenbox.
type relayDevice struct {
	deviceID      string
	userID        string
	channelID     string
	channelAuth   string // decrypted, kept in memory only for the duration of the send
	encryptionPub []byte // raw 32-byte X25519 public
}

// resolveChatRecipientCohorts splits each push-enabled user's active devices into
// the relay-eligible cohort (relay channel + encryption pub present, channel auth
// unseals cleanly) and the legacy notifier cohort (TokenCipher present, no relay
// channel). Devices that satisfy both criteria go on the relay path — a device that
// has registered with the relay should never also fan out via direct APNs/FCM,
// otherwise the user gets two notifications for the same chat.
func resolveChatRecipientCohorts(
	ctx context.Context,
) (relay []relayDevice, legacy []notifier.DeviceAddr) {
	if pushTokenKey == nil {
		return nil, nil
	}
	s := storage.Global()
	if s == nil {
		return nil, nil
	}
	allPrefs, err := hulabolt.ListNotificationPrefs(ctx, s)
	if err != nil {
		log.Warnf("chat push: list prefs: %v", err)
		return nil, nil
	}
	for _, p := range allPrefs {
		if !p.PushEnabled {
			continue
		}
		devs, err := hulabolt.ListDevicesForUser(ctx, s, p.UserID)
		if err != nil {
			continue
		}
		for _, d := range devs {
			if !d.Active {
				continue
			}
			// Relay path: requires channel id + sealed auth + encryption pub.
			if d.RelayChannelID != "" && len(d.RelayChannelAuthCipher) > 0 && d.NoiseEncryptionPub != "" {
				auth, err := tokenbox.Open(d.RelayChannelAuthCipher, pushTokenKey)
				if err != nil {
					log.Warnf("chat push: tokenbox open (relay auth) device=%s: %v", d.ID, err)
					continue
				}
				pubBytes, err := base64.StdEncoding.DecodeString(d.NoiseEncryptionPub)
				if err != nil {
					log.Warnf("chat push: decode noise pub device=%s: %v", d.ID, err)
					continue
				}
				if len(pubBytes) != 32 {
					log.Warnf(
						"chat push: noise pub wrong length device=%s: got %d",
						d.ID, len(pubBytes),
					)
					continue
				}
				relay = append(relay, relayDevice{
					deviceID:      d.ID,
					userID:        d.UserID,
					channelID:     d.RelayChannelID,
					channelAuth:   string(auth),
					encryptionPub: pubBytes,
				})
				continue
			}
			// Legacy path: requires sealed push token.
			if len(d.TokenCipher) == 0 {
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
			legacy = append(legacy, notifier.DeviceAddr{
				Channel:   ch,
				UserID:    d.UserID,
				DeviceID:  d.ID,
				PushToken: plain,
			})
		}
	}
	return relay, legacy
}

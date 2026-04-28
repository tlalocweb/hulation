// Package email is the notifier backend that wraps the Phase-3
// mailer (pkg/mailer). Translates an Envelope + per-recipient
// ChannelEmail DeviceAddrs into one SMTP send per recipient.
//
// Routing: each distinct DeviceAddr.Email becomes one Mailer.Send
// call. We don't batch into a single To: list because the Phase-3
// mailer already handles one-addressee-at-a-time and doing otherwise
// would expose every recipient's address to every other recipient.

package email

import (
	"context"

	"github.com/tlalocweb/hulation/pkg/mailer"
	"github.com/tlalocweb/hulation/pkg/notifier"
)

// Backend implements notifier.Notifier against pkg/mailer.
type Backend struct {
	m *mailer.Mailer
}

// New constructs an email backend. A nil mailer is legal — Deliver
// returns a single ErrNotConfigured entry in that case.
func New(m *mailer.Mailer) *Backend {
	return &Backend{m: m}
}

// Channel returns ChannelEmail.
func (b *Backend) Channel() notifier.Channel { return notifier.ChannelEmail }

// Deliver iterates the envelope's recipient list and sends one email
// per email address. Push-channel addrs are skipped silently.
func (b *Backend) Deliver(ctx context.Context, env notifier.Envelope) ([]notifier.ChannelResult, error) {
	var out []notifier.ChannelResult
	for _, r := range env.Recipients {
		if r.Channel != notifier.ChannelEmail {
			continue
		}
		if r.Email == "" {
			continue
		}
		if b.m == nil {
			out = append(out, notifier.ChannelResult{
				Channel: notifier.ChannelEmail,
				UserID:  r.UserID,
				OK:      false,
				Err:     notifier.ErrNotConfigured,
			})
			continue
		}
		err := b.m.Send(ctx, mailer.Message{
			To:      []string{r.Email},
			Subject: env.Subject,
			HTML:    env.HTMLBody,
		})
		res := notifier.ChannelResult{
			Channel: notifier.ChannelEmail,
			UserID:  r.UserID,
			OK:      err == nil,
			Err:     err,
		}
		if err == mailer.ErrNotConfigured {
			res.Err = notifier.ErrNotConfigured
		}
		out = append(out, res)
	}
	return out, nil
}

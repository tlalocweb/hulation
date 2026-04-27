// Package mailer sends email via SMTP using the stdlib net/smtp.
// Scoped for the Phase-3 scheduled-report dispatcher; keep the surface
// tiny and don't grow into a general-purpose mailer framework.
//
// Usage:
//
//   m := mailer.New(cfg.Mailer)
//   err := m.Send(ctx, mailer.Message{
//       To: []string{"bob@example.com"}, Subject: "…", HTML: "…",
//   })
//
// When cfg.Mailer isn't Configured(), Send returns ErrNotConfigured so
// callers can degrade to "log only" mode.

package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"

	"github.com/tlalocweb/hulation/config"
)

// ErrNotConfigured is returned by Send when the MailerConfig is
// missing the minimum fields.
var ErrNotConfigured = errors.New("mailer: SMTP not configured")

// Mailer wraps a MailerConfig with a thin convenience method.
type Mailer struct {
	cfg *config.MailerConfig
}

// New returns a Mailer. cfg may be nil — Send will return
// ErrNotConfigured in that case.
func New(cfg *config.MailerConfig) *Mailer { return &Mailer{cfg: cfg} }

// Message is the body of a single email.
type Message struct {
	To      []string
	Subject string
	HTML    string
}

// Send delivers the message via SMTP. Returns ErrNotConfigured when
// the underlying config is missing essentials.
func (m *Mailer) Send(ctx context.Context, msg Message) error {
	if m == nil || !m.cfg.Configured() {
		return ErrNotConfigured
	}
	if len(msg.To) == 0 {
		return errors.New("mailer: no recipients")
	}

	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	raw := buildRawMessage(m.cfg.From, msg)

	if m.cfg.StartTLS {
		return sendSTARTTLS(addr, m.cfg, msg.To, raw)
	}
	// Plaintext SMTP (or implicit TLS on :465 — not wired here; use
	// StartTLS=true on :587 which is the common modern default).
	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}
	return smtp.SendMail(addr, auth, extractFromAddress(m.cfg.From), msg.To, raw)
}

// sendSTARTTLS is smtp.SendMail with explicit STARTTLS handshake so
// providers like Gmail / SES / Mailgun accept us on :587.
func sendSTARTTLS(addr string, cfg *config.MailerConfig, to []string, raw []byte) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer client.Close()

	if err := client.Hello("localhost"); err != nil {
		return fmt.Errorf("ehlo: %w", err)
	}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if err := client.StartTLS(tlsCfg); err != nil {
		return fmt.Errorf("starttls: %w", err)
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	from := extractFromAddress(cfg.From)
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, r := range to {
		if err := client.Rcpt(r); err != nil {
			return fmt.Errorf("rcpt %s: %w", r, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return client.Quit()
}

// buildRawMessage returns the RFC-2822 body the SMTP DATA command
// expects. Headers + blank line + HTML body.
func buildRawMessage(from string, msg Message) []byte {
	var b strings.Builder
	if from != "" {
		b.WriteString("From: ")
		b.WriteString(from)
		b.WriteString("\r\n")
	}
	b.WriteString("To: ")
	b.WriteString(strings.Join(msg.To, ", "))
	b.WriteString("\r\n")
	b.WriteString("Subject: ")
	b.WriteString(msg.Subject)
	b.WriteString("\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(msg.HTML)
	return []byte(b.String())
}

// extractFromAddress pulls the bare email from a "Display <email@x>"
// form. The SMTP Mail command wants just the address.
func extractFromAddress(from string) string {
	if from == "" {
		return ""
	}
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.Index(from[i:], ">"); j > 0 {
			return from[i+1 : i+j]
		}
	}
	return from
}

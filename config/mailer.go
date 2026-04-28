package config

// MailerConfig — SMTP configuration for the Phase-3 report
// dispatcher. Kept minimal on purpose; the dispatcher only needs
// enough to open a connection and send an HTML email.
//
// YAML example:
//   mailer:
//     host: smtp.example.com
//     port: 587
//     username: hula@example.com
//     password: "{{env:SMTP_PASSWORD}}"
//     from: "Hula Analytics <hula@example.com>"
//     starttls: true

type MailerConfig struct {
	Host     string `yaml:"host,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
	From     string `yaml:"from,omitempty"`
	StartTLS bool   `yaml:"starttls,omitempty"`
}

// Configured reports whether the mailer block has the minimum
// fields the SMTP sender needs. Callers that get false should log
// a warning and skip send operations (but continue boot).
func (m *MailerConfig) Configured() bool {
	if m == nil {
		return false
	}
	return m.Host != "" && m.Port != 0 && m.From != ""
}

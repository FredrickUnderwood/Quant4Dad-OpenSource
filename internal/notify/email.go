package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// emailSender sends HTML-bodied email over SMTP.
type emailSender struct {
	cfg EmailConfig
}

func newEmailSender(cfg EmailConfig) *emailSender { return &emailSender{cfg: cfg} }

func (*emailSender) Channel() string { return ChannelEmail }

func (s *emailSender) Send(ctx context.Context, msg Message) error {
	to := msg.To
	if len(to) == 0 {
		to = s.cfg.DefaultTo
	}
	to = cleanAddrs(to)
	if len(to) == 0 {
		return errors.New("notify/email: no recipients (the node named none and there is no default)")
	}

	fromHeader := strings.TrimSpace(s.cfg.From)
	if fromHeader == "" {
		fromHeader = s.cfg.Username
	}
	if fromHeader == "" {
		return errors.New("notify/email: no sender (configure from or username)")
	}

	// An envelope address (MAIL FROM / RCPT TO) must be a bare addr@domain, with no display
	// name, angle brackets or whitespace, or some servers reject it with 502 Invalid input. The
	// display name belongs in the headers only.
	fromAddr, err := envelopeAddr(fromHeader)
	if err != nil {
		return fmt.Errorf("notify/email: invalid sender address %q: %w", fromHeader, err)
	}
	toAddrs := make([]string, 0, len(to))
	for _, t := range to {
		a, err := envelopeAddr(t)
		if err != nil {
			return fmt.Errorf("notify/email: invalid recipient address %q: %w", t, err)
		}
		toAddrs = append(toAddrs, a)
	}

	raw := buildMessage(fromHeader, to, msg.Title, msg.Body)
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))

	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	// Send from its own goroutine, using ctx for the overall timeout and cancellation, since
	// net/smtp does not take a context itself.
	done := make(chan error, 1)
	go func() {
		if s.cfg.UseSSL {
			done <- s.sendSSL(addr, auth, fromAddr, toAddrs, raw)
			return
		}
		// STARTTLS or plaintext: smtp.SendMail upgrades to STARTTLS when the server supports it.
		done <- smtp.SendMail(addr, auth, fromAddr, toAddrs, raw)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("notify/email: send failed: %w", err)
		}
		return nil
	}
}

// sendSSL connects with implicit TLS — typically port 465 — and then delivers. net/smtp
// defaults to plaintext or STARTTLS, so a pure SSL port needs tls.Dial followed by NewClient.
func (s *emailSender) sendSSL(addr string, auth smtp.Auth, from string, to []string, raw []byte) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{ServerName: s.cfg.Host})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return err
	}
	defer c.Close()

	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMessage assembles a minimal RFC 5322 message: a UTF-8 HTML body, with the subject MIME
// encoded so non-ASCII text is not mangled. The body goes out as text/html, so tags such as
// <br> written in the template take effect, and real newlines are converted to <br> so
// plaintext line breaks also break lines in a mail client.
func buildMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	// mime.BEncoding applies RFC 2047 Base64 encoding to a subject containing non-ASCII and
	// returns pure ASCII unchanged, which keeps non-ASCII subjects from being mangled.
	b.WriteString("Subject: " + mime.BEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(toHTMLBody(body))
	return []byte(b.String())
}

// toHTMLBody turns the body into an HTML fragment a mail client can render: first normalize
// the line endings, then replace real newlines with <br>. A <br> hand-written in the template
// is left as is, so both \n and <br> break lines.
func toHTMLBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.ReplaceAll(body, "\n", "<br>\n")
}

// envelopeAddr extracts the bare address for the SMTP envelope (MAIL FROM / RCPT TO) from
// input that may carry a display name, such as "Alerts <abc@example.com>" or plain
// "abc@example.com". An envelope address with a display name or stray whitespace is rejected
// by some servers with 502 Invalid input, so it is normalized here.
func envelopeAddr(s string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	return a.Address, nil
}

func cleanAddrs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

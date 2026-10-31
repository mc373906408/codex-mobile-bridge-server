package worker

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
)

type SMTP struct{ Config config.SMTP }

func (s SMTP) Send(ctx context.Context, message store.MailMessage) error {
	from, err := mail.ParseAddress(s.Config.From)
	if err != nil {
		return errors.New("invalid sender")
	}
	to, err := mail.ParseAddress(message.To)
	if err != nil || strings.ContainsAny(message.Subject, "\r\n") {
		return errors.New("invalid recipient")
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", s.Config.Address)
	if err != nil {
		return errors.New("SMTP unavailable")
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: s.Config.Host, MinVersion: tls.VersionTLS12}
	if s.Config.TLS == "implicit" {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			return errors.New("SMTP TLS failed")
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, s.Config.Host)
	if err != nil {
		return errors.New("SMTP unavailable")
	}
	defer client.Close()
	if s.Config.TLS == "starttls" {
		if err = client.StartTLS(tlsConfig); err != nil {
			return errors.New("SMTP TLS failed")
		}
	}
	if s.Config.User != "" {
		if err = client.Auth(smtp.PlainAuth("", s.Config.User, s.Config.Password, s.Config.Host)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return errors.New("SMTP rejected sender")
	}
	if err = client.Rcpt(to.Address); err != nil {
		return errors.New("SMTP rejected recipient")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP unavailable")
	}
	_, err = writer.Write([]byte("From: " + from.String() + "\r\nTo: " + to.String() + "\r\nSubject: " + message.Subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + message.Body))
	if err != nil {
		return errors.New("SMTP send failed")
	}
	if err = writer.Close(); err != nil {
		return errors.New("SMTP send result unknown")
	}
	return client.Quit()
}

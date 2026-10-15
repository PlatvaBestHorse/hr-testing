// Package maildelivery never logs recipients, message bodies or credentials.
package maildelivery

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Message struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}
type Sender struct{ Mode, Directory, Address, Username, Password, From string }

func (s Sender) Send(ctx context.Context, m Message) error {
	addr, err := mail.ParseAddress(m.To)
	if err != nil || strings.ContainsAny(m.Subject, "\r\n") {
		return fmt.Errorf("invalid mail")
	}
	content := []byte("To: " + addr.String() + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", m.Subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + strings.ReplaceAll(m.Body, "\n", "\r\n") + "\r\n")
	if s.Mode == "file" {
		if err = os.MkdirAll(s.Directory, 0700); err != nil {
			return fmt.Errorf("mailbox unavailable")
		}
		st, e := os.Lstat(s.Directory)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid mailbox")
		}
		if e = os.Chmod(s.Directory, 0700); e != nil {
			return e
		}
		id := make([]byte, 16)
		if _, e = rand.Read(id); e != nil {
			return e
		}
		path := filepath.Join(s.Directory, time.Now().UTC().Format("20060102T150405Z")+"-"+hex.EncodeToString(id)+".eml")
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return fmt.Errorf("mailbox write failed")
		}
		_, e = f.Write(content)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil || ce != nil {
			os.Remove(path)
			return fmt.Errorf("mailbox write failed")
		}
		return nil
	}
	if s.Mode != "smtp" {
		return fmt.Errorf("mail delivery disabled")
	}
	host, _, err := net.SplitHostPort(s.Address)
	if err != nil {
		return fmt.Errorf("invalid SMTP address")
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("invalid sender")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.Address)
	if err != nil {
		return fmt.Errorf("SMTP unavailable")
	}
	defer conn.Close()
	deadline := time.Now().Add(7 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP handshake failed")
	}
	defer client.Close()
	if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("SMTP TLS required")
	}
	if s.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return fmt.Errorf("SMTP authentication failed")
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return fmt.Errorf("SMTP sender rejected")
	}
	if err = client.Rcpt(addr.Address); err != nil {
		return fmt.Errorf("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP data failed")
	}
	if _, err = writer.Write(append([]byte("From: "+from.String()+"\r\n"), content...)); err != nil {
		return fmt.Errorf("SMTP write failed")
	}
	if err = writer.Close(); err != nil {
		return fmt.Errorf("SMTP acceptance failed")
	}
	// DATA acceptance is authoritative; a lost QUIT response must not resend mail.
	client.Quit()
	return nil
}

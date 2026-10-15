package maildelivery

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivateLocalMailbox(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "mail")
	sender := Sender{Mode: "file", Directory: directory}
	message := Message{To: "fixture@example.test", Subject: "Восстановление", Body: "Fixture message"}
	if err := sender.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 {
		t.Fatal("missing mail")
	}
	info, _ := files[0].Info()
	if info.Mode().Perm() != 0600 {
		t.Fatal("mail not private")
	}
	st, _ := os.Stat(directory)
	if st.Mode().Perm() != 0700 {
		t.Fatal("mailbox not private")
	}
	body, _ := os.ReadFile(filepath.Join(directory, files[0].Name()))
	if !strings.Contains(string(body), "Fixture message") || strings.Contains(string(body), "Subject: Восстановление") {
		t.Fatal("invalid message encoding")
	}
	message.Subject = "Header\r\nBcc: attacker@example.test"
	if err = sender.Send(context.Background(), message); err == nil {
		t.Fatal("accepted header injection")
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(directory, link)
	sender.Directory = link
	message.Subject = "Fixture"
	if err = sender.Send(context.Background(), message); err == nil {
		t.Fatal("accepted mailbox symlink")
	}
}
func TestSMTPRequiresSTARTTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		conn.Write([]byte("220 fixture SMTP\r\n"))
		r := bufio.NewReader(conn)
		r.ReadString('\n')
		conn.Write([]byte("250 fixture\r\n"))
		r.ReadString('\n')
		conn.Write([]byte("502 STARTTLS unavailable\r\n"))
	}()
	sender := Sender{Mode: "smtp", Address: listener.Addr().String(), From: "operator@example.test"}
	if err = sender.Send(context.Background(), Message{To: "fixture@example.test", Subject: "Fixture", Body: "Never send plaintext"}); err == nil {
		t.Fatal("SMTP accepted without TLS")
	}
	<-done
}

package mailbox

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

var serverTLS *tls.Config

func TestMain(m *testing.M) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certificate, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)

	directory, _ := os.MkdirTemp("", "scry-test-")
	certificatePath := filepath.Join(directory, "ca.pem")
	os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0o600)
	os.Setenv("SSL_CERT_FILE", certificatePath)

	serverTLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{certificate}, PrivateKey: key}}}

	code := m.Run()

	os.RemoveAll(directory)
	os.Exit(code)
}

func TestSync(t *testing.T) {
	var imageRequests atomic.Int32

	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imageRequests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("png"))
	}))
	defer imageServer.Close()

	memory := imapmemserver.New()
	user := imapmemserver.NewUser("me@example.com", "secret")
	memory.AddUser(user)

	for _, name := range []string{"INBOX", "Archive", "Events"} {
		user.Create(name, nil)
	}

	appendMessage := func(folder string, received time.Time, flags []imap.Flag, raw string) {
		content := []byte(strings.ReplaceAll(raw, "\n", "\r\n"))
		_, err := user.Append(folder, bytes.NewReader(content), &imap.AppendOptions{Flags: flags, Time: received})

		if err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()

	appendMessage("INBOX", now, nil, `From: Alex Moreno <alex@example.com>
To: me@example.com
Subject: Saturday walk?
Date: `+now.Format(time.RFC1123Z)+`
Content-Type: text/plain; charset=utf-8

Meet at the bridge car park.
`)

	appendMessage("INBOX", now.Add(-time.Hour), []imap.Flag{imap.FlagSeen}, `From: Northline Rail <tickets@example.com>
To: me@example.com
Subject: Your tickets
Date: `+now.Add(-time.Hour).Format(time.RFC1123Z)+`
Content-Type: multipart/related; boundary=b

--b
Content-Type: text/html; charset=utf-8

<p>Booking NR8K2QX</p><img src="cid:logo@example.com"><img src="`+imageServer.URL+`/track.png">
--b
Content-Type: image/png
Content-Id: <logo@example.com>
Content-Transfer-Encoding: base64

cG5n
--b--
`)

	appendMessage("Archive", now.AddDate(0, -1, 0), []imap.Flag{imap.FlagSeen}, `From: Skylark Air <noreply@example.com>
Subject: Trip confirmed
Content-Type: text/plain

Flight SK 214.
`)

	appendMessage("Archive", now.AddDate(-2, 0, 0), []imap.Flag{imap.FlagSeen}, `From: Skylark Air <noreply@example.com>
Subject: Old trip
Content-Type: text/plain

Flight SK 100.
`)

	appendMessage("Events", now, nil, `From: Hollow Lane Club <hello@example.net>
Subject: Winter social
Content-Type: text/plain

See you there.
`)

	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memory.NewSession(), nil, nil
		},
		Caps:      imap.CapSet{imap.CapIMAP4rev1: {}},
		TLSConfig: serverTLS,
		Logger:    discardLogger{},
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	go server.Serve(listener)
	defer server.Close()

	account := Account{Name: "Personal", Email: "me@example.com", IMAP: listener.Addr().String(), PasswordCommand: "echo secret", Folders: []string{"Events"}}
	mirrorDirectory := t.TempDir()
	mail := New([]Account{account}, mirrorDirectory, 365)

	subjects := func(box *Mailbox, folder string) string {
		var found []string

		for _, message := range box.Folder("Personal", folder) {
			marker := ""
			if message.Unread {
				marker = "*"
			}

			found = append(found, marker+message.Subject)
		}

		return strings.Join(found, ", ")
	}

	steps := []struct {
		name    string
		action  func() error
		wantErr string
		check   func(t *testing.T)
	}{
		{
			name:    "first sync",
			action:  func() error { return mail.Sync(context.Background(), t.Logf) },
			wantErr: "the server has no Sent folder",
			check: func(t *testing.T) {
				expected := map[string]string{
					Inbox:    "*Saturday walk?, Your tickets",
					Archive:  "Trip confirmed",
					"Events": "*Winter social",
				}

				for folder, want := range expected {
					if got := subjects(mail, folder); got != want {
						t.Errorf("%s: got %q, want %q", folder, got, want)
					}
				}

				if done, total := mail.SyncProgress(); done != 4 || total != 4 {
					t.Errorf("progress: got %d of %d, want 4 of 4", done, total)
				}
			},
		},
		{
			name:   "inline images and blocked loopback images",
			action: func() error { return nil },
			check: func(t *testing.T) {
				tickets := mail.Folder("Personal", Inbox)[1]

				loaded, err := mail.Load(tickets.ID)

				if err != nil {
					t.Fatal(err)
				}

				if !strings.Contains(loaded.HTML, "data:image/png;base64,cG5n") {
					t.Errorf("cid image not inlined: %s", loaded.HTML)
				}

				if !strings.Contains(loaded.HTML, imageServer.URL) || imageRequests.Load() != 0 {
					t.Errorf("loopback image was fetched: %d requests", imageRequests.Load())
				}
			},
		},
		{
			name: "read locally reaches the server",
			action: func() error {
				mail.MarkRead(mail.Folder("Personal", Inbox)[0].ID)
				return mail.Sync(context.Background(), t.Logf)
			},
			wantErr: "the server has no Sent folder",
			check: func(t *testing.T) {
				fresh := New([]Account{account}, t.TempDir(), 365)
				fresh.Sync(context.Background(), t.Logf)

				if got := subjects(fresh, Inbox); got != "Saturday walk?, Your tickets" {
					t.Errorf("server state: got %q", got)
				}
			},
		},
		{
			name: "server changes come back",
			action: func() error {
				client, err := imapclient.DialStartTLS(listener.Addr().String(), nil)

				if err != nil {
					return err
				}

				defer client.Close()

				client.Login("me@example.com", "secret").Wait()
				client.Select("Events", nil).Wait()
				client.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close()
				client.Select("INBOX", nil).Wait()
				client.Store(imap.SeqSetNum(2), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
				client.Expunge().Close()

				return mail.Sync(context.Background(), t.Logf)
			},
			wantErr: "the server has no Sent folder",
			check: func(t *testing.T) {
				if got := subjects(mail, Inbox); got != "Saturday walk?" {
					t.Errorf("inbox: got %q", got)
				}

				if got := subjects(mail, "Events"); got != "Winter social" {
					t.Errorf("events: got %q", got)
				}
			},
		},
		{
			name: "restart offline reads the mirror",
			action: func() error {
				server.Close()

				restarted := New([]Account{account}, mirrorDirectory, 365)
				err := restarted.Sync(context.Background(), t.Logf)

				mail = restarted

				return err
			},
			wantErr: "logging in",
			check: func(t *testing.T) {
				if got := subjects(mail, Archive); got != "Trip confirmed" {
					t.Errorf("archive: got %q", got)
				}

				loaded, err := mail.Load(mail.Folder("Personal", Archive)[0].ID)

				if err != nil || !strings.Contains(loaded.Text, "SK 214") {
					t.Errorf("load: %v %q", err, loaded.Text)
				}
			},
		},
		{
			name: "locked password store",
			action: func() error {
				locked := account
				locked.PasswordCommand = "false"

				return New([]Account{locked}, t.TempDir(), 365).Sync(context.Background(), t.Logf)
			},
			wantErr: "couldn't get the password",
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			err := step.action()

			switch {
			case step.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)

			case step.wantErr != "" && (err == nil || !strings.Contains(err.Error(), step.wantErr)):
				t.Fatalf("got error %v, want one containing %q", err, step.wantErr)
			}

			if step.check != nil {
				step.check(t)
			}
		})
	}
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

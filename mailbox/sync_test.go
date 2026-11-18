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
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
Message-ID: <walk@example.com>
References: <plan@example.com>
Content-Type: text/plain; charset=utf-8

Meet at the bridge car park.
`)

	appendMessage("INBOX", now, []imap.Flag{imap.FlagSeen}, `From: Northline Rail <tickets@example.com>
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
		Caps:      imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}, imap.CapIdle: {}},
		TLSConfig: serverTLS,
		Logger:    discardLogger{},
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	go server.Serve(listener)
	defer server.Close()

	account := Account{Name: "Personal", Email: "me@example.com", IMAP: listener.Addr().String(), PasswordCommand: []string{"echo", "secret"}, Folders: []string{"Events"}}
	mirrorDirectory := t.TempDir()
	mail := New([]Account{account}, mirrorDirectory, 365)

	var reports []string

	report := func(format string, arguments ...any) {
		reports = append(reports, fmt.Sprintf(format, arguments...))
		t.Logf(format, arguments...)
	}

	var arrived []Message

	sync := func(report func(format string, arguments ...any)) error {
		var err error
		arrived, err = mail.Sync(context.Background(), "Personal", report)
		return err
	}

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
		name        string
		action      func() error
		wantErr     string
		wantArrived string
		check       func(t *testing.T)
	}{
		{
			name:        "first sync",
			action:      func() error { return sync(t.Logf) },
			wantErr:     "the server has no Sent folder",
			wantArrived: "Saturday walk?",
			check: func(t *testing.T) {
				expected := map[string]string{
					Inbox:    "*Saturday walk?, Your tickets",
					Archive:  "Trip confirmed",
					"Events": "*Winter social",
				}

				for folder, want := range expected {
					got := subjects(mail, folder)

					if got != want {
						t.Errorf("%s: got %q, want %q", folder, got, want)
					}
				}

				walk := mail.Folder("Personal", Inbox)[0]

				if walk.MessageID != "walk@example.com" || strings.Join(walk.References, " ") != "plan@example.com" {
					t.Errorf("threading headers: got %q and %q", walk.MessageID, walk.References)
				}

				done, total := mail.SyncProgress("Personal")

				if done != 4 || total != 4 {
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
				return sync(t.Logf)
			},
			wantErr: "the server has no Sent folder",
			check: func(t *testing.T) {
				fresh := New([]Account{account}, t.TempDir(), 365)
				fresh.Sync(context.Background(), "Personal", t.Logf)

				got := subjects(fresh, Inbox)

				if got != "Saturday walk?, Your tickets" {
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

				return sync(t.Logf)
			},
			wantErr: "the server has no Sent folder",
			check: func(t *testing.T) {
				got := subjects(mail, Inbox)

				if got != "Saturday walk?" {
					t.Errorf("inbox: got %q", got)
				}

				got = subjects(mail, "Events")

				if got != "Winter social" {
					t.Errorf("events: got %q", got)
				}
			},
		},
		{
			name: "read and archive survive a restart",
			action: func() error {
				received := now.AddDate(0, -2, 0)

				appendMessage("INBOX", received, nil, `From: Pine Street Lettings <office@example.org>
Subject: Quarterly report
Date: `+received.Format(time.RFC1123Z)+`
Content-Type: text/plain

Rent statement attached.
`)

				sync(t.Logf)

				if len(arrived) > 0 {
					return fmt.Errorf("an email received two months ago arrived: %q", arrived[0].Subject)
				}

				quarterly := mail.Folder("Personal", Inbox)[1]

				mail.MarkRead(quarterly.ID)

				err := mail.Move(quarterly.ID, Archive)

				if err != nil {
					return err
				}

				err = mail.Save()

				if err != nil {
					return err
				}

				mail = New([]Account{account}, mirrorDirectory, 365)

				got := subjects(mail, Archive)

				if got != "" {
					t.Errorf("restarted mailbox opened before sync: %q", got)
				}

				return sync(report)
			},
			wantErr: "the server has no Sent folder",
			check: func(t *testing.T) {
				fresh := New([]Account{account}, t.TempDir(), 365)
				fresh.Sync(context.Background(), "Personal", t.Logf)

				for _, box := range []*Mailbox{mail, fresh} {
					inbox := subjects(box, Inbox)
					archive := subjects(box, Archive)

					if inbox != "Saturday walk?" || archive != "Trip confirmed, Quarterly report" {
						t.Errorf("got inbox %q and archive %q", inbox, archive)
					}
				}

				loaded, err := mail.Load(mail.Folder("Personal", Archive)[1].ID)

				if err != nil || !strings.Contains(loaded.Text, "Rent statement") {
					t.Errorf("load: %v %q", err, loaded.Text)
				}

				if !slices.Contains(reports, "Inbox: moved 1, dropped 0 already moved or deleted elsewhere") {
					t.Errorf("reports: %q", reports)
				}
			},
		},
		{
			name: "move dropped when deleted elsewhere",
			action: func() error {
				err := mail.Move(mail.Folder("Personal", Inbox)[0].ID, "Events")

				if err != nil {
					return err
				}

				client, err := imapclient.DialStartTLS(listener.Addr().String(), nil)

				if err != nil {
					return err
				}

				defer client.Close()

				client.Login("me@example.com", "secret").Wait()
				client.Select("INBOX", nil).Wait()
				client.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
				client.Expunge().Close()

				reports = nil

				return sync(report)
			},
			wantErr: "the server has no Sent folder",
			check: func(t *testing.T) {
				inbox := subjects(mail, Inbox)
				events := subjects(mail, "Events")

				if inbox != "" || events != "Winter social" {
					t.Errorf("got inbox %q and events %q", inbox, events)
				}

				if !slices.Contains(reports, "Inbox: moved 0, dropped 1 already moved or deleted elsewhere") {
					t.Errorf("reports: %q", reports)
				}
			},
		},
		{
			name: "inbox watch wakes on new mail",
			action: func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				waiting := make(chan struct{}, 1)
				watched := make(chan error, 1)

				watchReport := func(format string, arguments ...any) {
					if strings.Contains(format, "waiting") {
						waiting <- struct{}{}
					}
				}

				go func() { watched <- mail.WatchInbox(ctx, "Personal", watchReport) }()

				select {
				case <-waiting:

				case err := <-watched:
					return err
				}

				time.Sleep(100 * time.Millisecond)

				appendMessage("INBOX", now, nil, "From: Alex Moreno <alex@example.com>\nSubject: One more thing\n\nBring a torch.\n")

				return <-watched
			},
		},
		{
			name: "read dropped when deleted elsewhere",
			action: func() error {
				mail.Sync(context.Background(), "Personal", t.Logf)

				inbox := mail.Folder("Personal", Inbox)

				if len(inbox) != 1 {
					return fmt.Errorf("inbox holds %d emails, want 1", len(inbox))
				}

				mail.MarkRead(inbox[0].ID)

				client, err := imapclient.DialStartTLS(listener.Addr().String(), nil)

				if err != nil {
					return err
				}

				defer client.Close()

				client.Login("me@example.com", "secret").Wait()
				client.Select("INBOX", nil).Wait()
				client.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
				client.Expunge().Close()

				reports = nil

				return sync(report)
			},
			wantErr: "the server has no Sent folder",
			check: func(t *testing.T) {
				inbox := subjects(mail, Inbox)

				if inbox != "" {
					t.Errorf("inbox: got %q", inbox)
				}

				if !slices.Contains(reports, "Inbox: marked 0 read on the server, dropped 1 already moved or deleted elsewhere") {
					t.Errorf("reports: %q", reports)
				}
			},
		},
		{
			name: "restart offline reads the mirror",
			action: func() error {
				server.Close()

				restarted := New([]Account{account}, mirrorDirectory, 365)
				_, err := restarted.Sync(context.Background(), "Personal", t.Logf)

				mail = restarted

				return err
			},
			wantErr: "logging in",
			check: func(t *testing.T) {
				got := subjects(mail, Archive)

				if got != "Trip confirmed, Quarterly report" {
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
				locked.PasswordCommand = []string{"false"}

				_, err := New([]Account{locked}, t.TempDir(), 365).Sync(context.Background(), "Personal", t.Logf)

				return err
			},
			wantErr: "couldn't get the password",
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			arrived = nil
			err := step.action()

			switch {
			case step.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)

			case step.wantErr != "" && (err == nil || !strings.Contains(err.Error(), step.wantErr)):
				t.Fatalf("got error %v, want one containing %q", err, step.wantErr)
			}

			var arrivedSubjects []string

			for _, message := range arrived {
				arrivedSubjects = append(arrivedSubjects, message.Subject)
			}

			if got := strings.Join(arrivedSubjects, ", "); got != step.wantArrived {
				t.Errorf("arrived: got %q, want %q", got, step.wantArrived)
			}

			if step.check != nil {
				step.check(t)
			}
		})
	}
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

func TestSpamAndTrash(t *testing.T) {
	user := imapmemserver.NewUser("me@example.com", "secret")

	for _, name := range []string{"INBOX", "Sent", "Archive", "Junk", "Deleted"} {
		user.Create(name, nil)
	}

	appendMessage := func(folder, raw string) {
		_, err := user.Append(folder, strings.NewReader(strings.ReplaceAll(raw, "\n", "\r\n")), &imap.AppendOptions{Time: time.Now()})

		if err != nil {
			t.Fatal(err)
		}
	}

	appendMessage("Junk", "From: Prize Desk <win@example.com>\nSubject: You won\nContent-Type: text/html\n\n<p>Claim it</p><img src=\"http://127.0.0.1:9/pixel.png\">\n")
	appendMessage("Deleted", "From: Corner Shop <receipts@example.org>\nSubject: Old receipt\nContent-Type: text/html\n\n<img src=\"http://127.0.0.1:9/logo.png\">\n")

	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return sentFolderSession{imapmemserver.NewUserSession(user)}, nil, nil
		},
		Caps:      imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}},
		TLSConfig: serverTLS,
		Logger:    discardLogger{},
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	go server.Serve(listener)
	defer server.Close()

	account := Account{Name: "Personal", Email: "me@example.com", IMAP: listener.Addr().String(), PasswordCommand: []string{"echo", "secret"}}
	mail := New([]Account{account}, t.TempDir(), 365)

	var reports []string

	report := func(format string, arguments ...any) {
		reports = append(reports, fmt.Sprintf(format, arguments...))
		t.Logf(format, arguments...)
	}

	sync := func() error {
		reports = nil
		_, err := mail.Sync(context.Background(), "Personal", report)
		return err
	}

	steps := []struct {
		name        string
		action      func() error
		wantReports []string
		wantFolders map[string]int
	}{
		{
			name:        "nothing is fetched for Spam or Trash",
			action:      sync,
			wantReports: []string{"Spam: remote images: 1 skipped in Spam", "Trash: remote images: 1 skipped in Trash"},
			wantFolders: map[string]int{Inbox: 0, Spam: 1, Trash: 1},
		},
		{
			name: "an email moved out of Spam gets its images",
			action: func() error {
				err := mail.Move(mail.Folder("Personal", Spam)[0].ID, Inbox)

				if err != nil {
					return err
				}

				return sync()
			},
			wantReports: []string{"Spam: remote images in 1 emails moved out: 1 refused private address"},
			wantFolders: map[string]int{Inbox: 1, Spam: 0, Trash: 1},
		},
		{
			name: "emptying Trash also deletes an email still on its way there",
			action: func() error {
				err := mail.Move(mail.Folder("Personal", Inbox)[0].ID, Trash)

				if err != nil {
					return err
				}

				emptied := mail.EmptyTrash("Personal")

				if emptied != 2 || len(mail.Folder("Personal", Trash)) != 0 || len(mail.All()) != 0 {
					return fmt.Errorf("emptied %d, Trash holds %d, search sees %d", emptied, len(mail.Folder("Personal", Trash)), len(mail.All()))
				}

				return sync()
			},
			wantReports: []string{"Trash: deleted 2 for good, dropped 0 already deleted elsewhere"},
			wantFolders: map[string]int{Inbox: 0, Spam: 0, Trash: 0},
		},
		{
			name: "the server no longer holds them",
			action: func() error {
				mail = New([]Account{account}, t.TempDir(), 365)
				return sync()
			},
			wantFolders: map[string]int{Inbox: 0, Spam: 0, Trash: 0},
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			err := step.action()

			if err != nil {
				t.Fatal(err)
			}

			for _, want := range step.wantReports {
				if !slices.Contains(reports, want) {
					t.Errorf("missing report %q", want)
				}
			}

			for folder, want := range step.wantFolders {
				got := len(mail.Folder("Personal", folder))

				if got != want {
					t.Errorf("%s holds %d, want %d", folder, got, want)
				}
			}
		})
	}
}

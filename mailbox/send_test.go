package mailbox

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-message/mail"
)

func TestSend(t *testing.T) {
	type delivery struct {
		from       string
		recipients []string
		data       []byte
	}

	deliveries := make(chan delivery, 8)

	var smtpPassword atomic.Value

	memory := imapmemserver.New()
	user := imapmemserver.NewUser("me@example.com", "secret")
	memory.AddUser(user)
	user.Create("INBOX", nil)

	imapServer := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memory.NewSession(), nil, nil
		},
		Caps:      imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}},
		TLSConfig: serverTLS,
		Logger:    discardLogger{},
	})

	imapListener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	go imapServer.Serve(imapListener)
	defer imapServer.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	defer listener.Close()

	go func() {
		for {
			connection, err := listener.Accept()

			if err != nil {
				return
			}

			go func() {
				defer connection.Close()

				current := connection
				reader := textproto.NewReader(bufio.NewReader(current))
				received := delivery{}

				fmt.Fprint(current, "220 localhost\r\n")

				for {
					line, err := reader.ReadLine()

					if err != nil {
						return
					}

					verb, argument, _ := strings.Cut(line, " ")

					switch strings.ToUpper(verb) {
					case "EHLO":
						fmt.Fprint(current, "250-localhost\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")

					case "STARTTLS":
						fmt.Fprint(current, "220 ready\r\n")
						current = tls.Server(connection, serverTLS)
						reader = textproto.NewReader(bufio.NewReader(current))

					case "AUTH":
						credentials, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(argument, "PLAIN "))

						if string(credentials) != "\x00me@example.com\x00"+smtpPassword.Load().(string) {
							fmt.Fprint(current, "535 invalid credentials\r\n")
							continue
						}

						fmt.Fprint(current, "235 ok\r\n")

					case "MAIL":
						received.from = argument
						fmt.Fprint(current, "250 ok\r\n")

					case "RCPT":
						received.recipients = append(received.recipients, argument)
						fmt.Fprint(current, "250 ok\r\n")

					case "DATA":
						fmt.Fprint(current, "354 go ahead\r\n")

						received.data, err = reader.ReadDotBytes()

						if err != nil {
							return
						}

						deliveries <- received
						fmt.Fprint(current, "250 ok\r\n")

					case "QUIT":
						fmt.Fprint(current, "221 bye\r\n")
						return

					default:
						fmt.Fprint(current, "250 ok\r\n")
					}
				}
			}()
		}
	}()

	directory := t.TempDir()
	signaturePath := filepath.Join(directory, "signature.html")
	attachmentPath := filepath.Join(directory, "notes.txt")

	os.WriteFile(signaturePath, []byte("<p>Kit Hale<br>Hollow Lane Club</p>"), 0o600)
	os.WriteFile(attachmentPath, []byte("Bring boots."), 0o600)

	account := Account{Name: "Personal", Email: "me@example.com", IMAP: imapListener.Addr().String(), SMTP: listener.Addr().String(), Signature: signaturePath, PasswordCommand: "echo secret"}

	cases := []struct {
		name         string
		password     string
		draft        Draft
		wantQueueErr string
		unqueue      bool
		wantErr      string
		want         []string
	}{
		{
			name:     "returned to the draft before sending",
			password: "secret",
			draft:    Draft{Account: "Personal", To: "alex@example.com", Subject: "Re: Saturday walk?", Body: "Yes.", Attach: []string{attachmentPath}, ReplyTo: "original"},
			unqueue:  true,
		},
		{
			name:     "new email with markdown, signature and attachment",
			password: "secret",
			draft: Draft{
				Account: "Personal",
				To:      "Alex Moreno <alex@example.com>, sam@example.net",
				Subject: "Saturday walk",
				Body:    "Hi **Alex**,\nsee <you> at 10** sharp.\n\n- boots\n- **map**\n\nKit",
				Attach:  []string{attachmentPath},
			},
			want: []string{
				"from FROM:<me@example.com>\n",
				"recipients TO:<alex@example.com> TO:<sam@example.net>\n",
				"message-id host example.com\n",
				"in-reply-to \n",
				"text/plain: Hi **Alex**,\nsee <you> at 10** sharp.\n\n- boots\n- **map**\n\nKit\n\nKit Hale\nHollow Lane Club\n",
				"text/html: <p>Hi <strong>Alex</strong>,<br>see &lt;you&gt; at 10** sharp.</p><ul><li>boots</li><li><strong>map</strong></li></ul><p>Kit</p><p>Kit Hale<br>Hollow Lane Club</p>\n",
				"attachment notes.txt: Bring boots.\n",
			},
		},
		{
			name:     "reply threads under the original",
			password: "secret",
			draft:    Draft{Account: "Personal", To: "alex@example.com", Subject: "Re: Saturday walk?", Body: "Yes.", ReplyTo: "original"},
			want: []string{
				"in-reply-to <walk@example.com>\n",
				"references <plan@example.com> <walk@example.com>\n",
			},
		},
		{
			name:     "smtp refuses the password",
			password: "other",
			draft:    Draft{Account: "Personal", To: "alex@example.com", Subject: "Hello", Body: "Hi."},
			wantErr:  "535",
		},
		{
			name:         "unreadable To line",
			password:     "secret",
			draft:        Draft{Account: "Personal", To: "not an address", Subject: "Hello", Body: "Hi."},
			wantQueueErr: "reading the To line",
		},
		{
			name:         "missing attachment",
			password:     "secret",
			draft:        Draft{Account: "Personal", To: "alex@example.com", Subject: "Hello", Body: "Hi.", Attach: []string{filepath.Join(directory, "gone.pdf")}},
			wantQueueErr: "reading the attachment gone.pdf",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			smtpPassword.Store(test.password)

			box := New([]Account{account}, t.TempDir(), 365)
			box.Sync(context.Background(), account.Name, t.Logf)
			box.messages = append(box.messages, Message{ID: "original", Account: account.Name, MessageID: "walk@example.com", References: []string{"plan@example.com"}})

			err := box.Queue(test.draft)

			switch {
			case test.wantQueueErr == "" && err != nil:
				t.Fatalf("unexpected queue error: %v", err)

			case test.wantQueueErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantQueueErr)):
				t.Fatalf("got queue error %v, want one containing %q", err, test.wantQueueErr)

			case test.wantQueueErr != "":
				if len(box.Folder(account.Name, Outbox)) != 0 {
					t.Fatal("a refused draft reached the outbox")
				}

				return
			}

			if len(box.Folder(account.Name, Outbox)) != 1 {
				t.Fatal("the queued email isn't in the outbox")
			}

			if test.unqueue {
				draft, err := box.Unqueue(box.Folder(account.Name, Outbox)[0].ID)

				if err != nil {
					t.Fatal(err)
				}

				if fmt.Sprint(draft) != fmt.Sprint(test.draft) {
					t.Errorf("got draft %+v, want %+v", draft, test.draft)
				}

				box.Sync(context.Background(), account.Name, t.Logf)

				select {
				case <-deliveries:
					t.Fatal("an email returned to the draft was sent")

				default:
				}

				if len(box.Folder(account.Name, Outbox)) != 0 {
					t.Fatal("an email returned to the draft stayed in the outbox")
				}

				return
			}

			err = box.Sync(context.Background(), account.Name, t.Logf)

			if err == nil || !strings.Contains(err.Error(), "the server has no Sent folder") {
				t.Fatalf("got sync error %v, want the missing Sent folder", err)
			}

			sendFailed := strings.Contains(err.Error(), "sending:")

			switch {
			case test.wantErr == "" && sendFailed:
				t.Fatalf("unexpected error: %v", err)

			case test.wantErr != "" && !strings.Contains(err.Error(), test.wantErr):
				t.Fatalf("got error %v, want one containing %q", err, test.wantErr)

			case test.wantErr != "":
				select {
				case <-deliveries:
					t.Fatal("a failed send delivered an email")

				default:
				}

				if len(box.Folder(account.Name, Outbox)) != 1 {
					t.Fatal("a failed send left the outbox")
				}

				return
			}

			if len(box.Folder(account.Name, Outbox)) != 0 {
				t.Fatal("a sent email stayed in the outbox")
			}

			var received delivery

			select {
			case received = <-deliveries:

			case <-time.After(5 * time.Second):
				t.Fatal("the server received nothing")
			}

			reader, err := mail.CreateReader(strings.NewReader(string(received.data)))

			if err != nil {
				t.Fatal(err)
			}

			messageID, _ := reader.Header.MessageID()
			_, messageHost, _ := strings.Cut(messageID, "@")

			var summary strings.Builder

			fmt.Fprintf(&summary, "from %s\n", received.from)
			fmt.Fprintf(&summary, "recipients %s\n", strings.Join(received.recipients, " "))
			fmt.Fprintf(&summary, "message-id host %s\n", messageHost)
			fmt.Fprintf(&summary, "in-reply-to %s\n", reader.Header.Get("In-Reply-To"))
			fmt.Fprintf(&summary, "references %s\n", reader.Header.Get("References"))

			for {
				part, err := reader.NextPart()

				if errors.Is(err, io.EOF) {
					break
				}

				if err != nil {
					t.Fatal(err)
				}

				body, _ := io.ReadAll(part.Body)
				text := strings.ReplaceAll(string(body), "\r\n", "\n")

				switch header := part.Header.(type) {
				case *mail.InlineHeader:
					contentType, _, _ := header.ContentType()
					fmt.Fprintf(&summary, "%s: %s\n", contentType, text)

				case *mail.AttachmentHeader:
					filename, _ := header.Filename()
					fmt.Fprintf(&summary, "attachment %s: %s\n", filename, text)
				}
			}

			for _, want := range test.want {
				if !strings.Contains(summary.String(), want) {
					t.Errorf("missing %q in:\n%s", want, summary.String())
				}
			}
		})
	}
}

package mailbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/sync/errgroup"

	"github.com/siliconwitch/scry/imapsync"
	"github.com/siliconwitch/scry/mirror"
)

const (
	Inbox   = "Inbox"
	Sent    = "Sent"
	Archive = "Archive"
	Spam    = "Spam"
	Trash   = "Trash"
)

var ErrAlreadyThere = errors.New("already in that folder")

type Address struct {
	Name  string
	Email string
}

func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}

	return a.Name + " <" + a.Email + ">"
}

func (a Address) Short() string {
	if a.Name == "" {
		return a.Email
	}

	return a.Name
}

type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

type Message struct {
	ID          string
	Account     string
	Folder      string
	From        Address
	To          []Address
	Subject     string
	Date        time.Time
	Unread      bool
	Text        string
	HTML        string
	Attachments []Attachment
	UID         uint32
}

type Account struct {
	Name            string
	Email           string
	IMAP            string
	PasswordCommand string
	Folders         []string
}

type Draft struct {
	Account string
	To      string
	Subject string
	Attach  []string
	Body    string
	ReplyTo string
}

type Mailbox struct {
	mutex           sync.Mutex
	accounts        []Account
	messages        []Message
	mirrorDirectory string
	historyDays     int
	opened          map[string]*openedAccount
	pendingSeen     map[string]bool
	syncDone        int
	syncTotal       int
}

type openedAccount struct {
	password string
	mirror   *mirror.Mirror
	folders  map[string]folderIndex
}

type folderIndex struct {
	Server      string
	UIDValidity uint32
}

type accountIndex struct {
	Folders  map[string]folderIndex
	Messages []Message
}

type storedBody struct {
	HTML        string
	Attachments []Attachment
}

func New(accounts []Account, mirrorDirectory string, historyDays int) *Mailbox {
	var withStandardFolders []Account

	for _, account := range accounts {
		account.Folders = append([]string{Inbox, Sent, Archive, Spam, Trash}, account.Folders...)
		withStandardFolders = append(withStandardFolders, account)
	}

	return &Mailbox{
		accounts:        withStandardFolders,
		mirrorDirectory: mirrorDirectory,
		historyDays:     historyDays,
		opened:          map[string]*openedAccount{},
		pendingSeen:     map[string]bool{},
	}
}

func (b *Mailbox) Accounts() []Account {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return slices.Clone(b.accounts)
}

func (b *Mailbox) Account(name string) (Account, bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for _, account := range b.accounts {
		if account.Name == name {
			return account, true
		}
	}

	return Account{}, false
}

func (b *Mailbox) Folder(account, folder string) []Message {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	var found []Message

	for _, message := range b.messages {
		if message.Folder != folder || (account != "" && message.Account != account) {
			continue
		}

		found = append(found, message)
	}

	slices.SortStableFunc(found, func(a, b Message) int { return b.Date.Compare(a.Date) })

	return found
}

func (b *Mailbox) All() []Message {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return slices.Clone(b.messages)
}

func (b *Mailbox) Get(id string) (Message, bool) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for _, message := range b.messages {
		if message.ID == id {
			return message, true
		}
	}

	return Message{}, false
}

func (b *Mailbox) Load(id string) (Message, error) {
	b.mutex.Lock()
	index := slices.IndexFunc(b.messages, func(m Message) bool { return m.ID == id })

	if index < 0 {
		b.mutex.Unlock()
		return Message{}, fmt.Errorf("message %s not found", id)
	}

	message := b.messages[index]
	opened := b.opened[message.Account]
	b.mutex.Unlock()

	content, err := opened.mirror.Read(bodyFile(id))

	if err != nil {
		return message, err
	}

	var body storedBody

	err = gob.NewDecoder(bytes.NewReader(content)).Decode(&body)

	if err != nil {
		return message, err
	}

	message.HTML = body.HTML
	message.Attachments = body.Attachments

	return message, nil
}

func (b *Mailbox) Unread(account, folder string) int {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	count := 0

	for _, message := range b.messages {
		if message.Unread && message.Folder == folder && (account == "" || message.Account == account) {
			count++
		}
	}

	return count
}

func (b *Mailbox) Move(id, folder string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	index := slices.IndexFunc(b.messages, func(m Message) bool { return m.ID == id })

	if index < 0 {
		return fmt.Errorf("message %s not found", id)
	}

	message := &b.messages[index]

	if message.Folder == folder {
		return ErrAlreadyThere
	}

	accountIndex := slices.IndexFunc(b.accounts, func(a Account) bool { return a.Name == message.Account })

	if accountIndex < 0 || !slices.Contains(b.accounts[accountIndex].Folders, folder) {
		return fmt.Errorf("%s has no %s folder", message.Account, folder)
	}

	return errors.New("moving isn't built yet")
}

func (b *Mailbox) MarkRead(id string) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for i := range b.messages {
		if b.messages[i].ID == id {
			b.messages[i].Unread = false
			b.pendingSeen[id] = true
		}
	}
}

func (b *Mailbox) SyncProgress() (done, total int) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.syncDone, b.syncTotal
}

func (b *Mailbox) Sync(ctx context.Context, report func(format string, arguments ...any)) error {
	type plannedFolder struct {
		folder      string
		server      string
		uidValidity uint32
		fresh       []uint32
	}

	type accountPlan struct {
		account Account
		opened  *openedAccount
		folders []plannedFolder
	}

	var plans []accountPlan

	b.mutex.Lock()
	b.syncDone = 0
	b.syncTotal = 0
	b.mutex.Unlock()

	carrierGradeNAT := netip.MustParsePrefix("100.64.0.0/10")

	imageClient := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
				Control: func(network, address string, _ syscall.RawConn) error {
					host, _, err := net.SplitHostPort(address)

					if err != nil {
						return err
					}

					ip, err := netip.ParseAddr(host)

					if err != nil {
						return err
					}

					ip = ip.Unmap()

					if !ip.IsGlobalUnicast() || ip.IsPrivate() || carrierGradeNAT.Contains(ip) {
						return fmt.Errorf("refusing to fetch an image from %s", ip)
					}

					return nil
				},
			}).DialContext,
			ForceAttemptHTTP2:   true,
			MaxIdleConnsPerHost: 4,
		},
	}

	var failures []error

	for _, account := range b.Accounts() {
		err := func() error {
			b.mutex.Lock()
			opened := b.opened[account.Name]
			b.mutex.Unlock()

			if opened == nil {
				words := strings.Fields(account.PasswordCommand)

				if len(words) == 0 {
					return errors.New("the config has no password-cmd")
				}

				output, err := exec.CommandContext(ctx, words[0], words[1:]...).Output()

				if err != nil {
					return errors.New("couldn't get the password. Unlock your password store and press Ctrl+R")
				}

				firstLine, _, _ := strings.Cut(string(output), "\n")
				password := strings.TrimSuffix(firstLine, "\r")

				if password == "" {
					return errors.New("password-cmd printed no password")
				}

				directory := filepath.Join(b.mirrorDirectory, fmt.Sprintf("%x", sha256.Sum256([]byte(account.Email)))[:16])

				store, err := mirror.Open(directory, []byte(password))

				if errors.Is(err, mirror.ErrWrongPassword) {
					session, loginErr := imapsync.Dial(ctx, account.IMAP, account.Email, password)

					if loginErr != nil {
						return fmt.Errorf("the password opens neither the mirror nor the server: %w", loginErr)
					}

					session.Close()

					err = mirror.Wipe(directory)

					if err != nil {
						return err
					}

					store, err = mirror.Open(directory, []byte(password))
				}

				if err != nil {
					return err
				}

				index := accountIndex{}

				content, err := store.Read("index")

				switch {
				case errors.Is(err, fs.ErrNotExist):

				case err != nil:
					return err

				default:
					err = gob.NewDecoder(bytes.NewReader(content)).Decode(&index)

					if err != nil {
						return fmt.Errorf("reading the mirror index: %w", err)
					}
				}

				if index.Folders == nil {
					index.Folders = map[string]folderIndex{}
				}

				opened = &openedAccount{password: password, mirror: store, folders: index.Folders}

				b.mutex.Lock()

				for _, message := range index.Messages {
					message.Account = account.Name
					b.messages = append(b.messages, message)
				}

				b.opened[account.Name] = opened
				b.mutex.Unlock()
			}

			plan := accountPlan{account: account, opened: opened}
			defer func() { plans = append(plans, plan) }()

			session, err := imapsync.Dial(ctx, account.IMAP, account.Email, opened.password)

			if err != nil {
				return fmt.Errorf("logging in: %w", err)
			}

			defer session.Close()

			serverFolders, err := session.Folders()

			if err != nil {
				return fmt.Errorf("listing folders: %w", err)
			}

			var folderFailures []error

			for _, folder := range account.Folders {
				specialUse := map[string]string{Sent: `\Sent`, Spam: `\Junk`, Trash: `\Trash`, Archive: `\Archive`}[folder]

				server := ""

				if folder == Inbox {
					server = "INBOX"
				}

				for _, candidate := range serverFolders {
					if specialUse != "" && candidate.SpecialUse == specialUse {
						server = candidate.Name
					}
				}

				if server == "" && (specialUse == "" || folder == Archive) && folder != Inbox {
					if slices.ContainsFunc(serverFolders, func(candidate imapsync.Folder) bool { return candidate.Name == folder }) {
						server = folder
					}
				}

				if server == "" {
					folderFailures = append(folderFailures, fmt.Errorf("the server has no %s folder", folder))
					continue
				}

				uidValidity, err := session.Select(server)

				if err != nil {
					folderFailures = append(folderFailures, fmt.Errorf("opening %s: %w", folder, err))
					continue
				}

				known := opened.folders[folder]

				if known.Server != server || known.UIDValidity != uidValidity {
					var dropped []string

					b.mutex.Lock()

					b.messages = slices.DeleteFunc(b.messages, func(m Message) bool {
						drop := m.Account == account.Name && m.Folder == folder

						if drop {
							dropped = append(dropped, m.ID)
						}

						return drop
					})

					opened.folders[folder] = folderIndex{Server: server, UIDValidity: uidValidity}

					b.mutex.Unlock()

					for _, id := range dropped {
						opened.mirror.Remove(bodyFile(id))
					}
				}

				since := time.Time{}

				if folder == Archive || folder == Sent {
					since = time.Now().AddDate(0, 0, -b.historyDays)
				}

				uids, err := session.Search(since)

				if err != nil {
					folderFailures = append(folderFailures, fmt.Errorf("searching %s: %w", folder, err))
					continue
				}

				knownUIDs := map[uint32]bool{}
				var pendingIDs []string
				var pendingUIDs []uint32

				b.mutex.Lock()

				for _, message := range b.messages {
					if message.Account != account.Name || message.Folder != folder {
						continue
					}

					knownUIDs[message.UID] = true

					if b.pendingSeen[message.ID] {
						pendingIDs = append(pendingIDs, message.ID)
						pendingUIDs = append(pendingUIDs, message.UID)
					}
				}

				b.mutex.Unlock()

				err = session.MarkSeen(pendingUIDs)

				if err != nil {
					folderFailures = append(folderFailures, fmt.Errorf("marking email read in %s: %w", folder, err))
					continue
				}

				seen, err := session.Seen(uids)

				if err != nil {
					folderFailures = append(folderFailures, fmt.Errorf("reading flags in %s: %w", folder, err))
					continue
				}

				var fresh []uint32

				for _, uid := range uids {
					if !knownUIDs[uid] {
						fresh = append(fresh, uid)
					}
				}

				var removed []string

				b.mutex.Lock()

				for _, id := range pendingIDs {
					delete(b.pendingSeen, id)
				}

				b.messages = slices.DeleteFunc(b.messages, func(m Message) bool {
					_, onServer := seen[m.UID]
					remove := m.Account == account.Name && m.Folder == folder && !onServer

					if remove {
						removed = append(removed, m.ID)
					}

					return remove
				})

				for i, message := range b.messages {
					if message.Account == account.Name && message.Folder == folder && !b.pendingSeen[message.ID] {
						b.messages[i].Unread = !seen[message.UID]
					}
				}

				b.mutex.Unlock()

				for _, id := range removed {
					opened.mirror.Remove(bodyFile(id))
				}

				plan.folders = append(plan.folders, plannedFolder{folder: folder, server: server, uidValidity: uidValidity, fresh: fresh})
			}

			err = b.saveIndex(account.Name, opened)

			if err != nil {
				folderFailures = append(folderFailures, err)
			}

			return errors.Join(folderFailures...)
		}()

		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", account.Name, err))
		}
	}

	total := 0

	for _, plan := range plans {
		for _, planned := range plan.folders {
			total += len(planned.fresh)
		}
	}

	b.mutex.Lock()
	b.syncTotal = total
	b.mutex.Unlock()

	for _, plan := range plans {
		account := plan.account
		opened := plan.opened

		if !slices.ContainsFunc(plan.folders, func(planned plannedFolder) bool { return len(planned.fresh) > 0 }) {
			continue
		}

		err := func() error {
			session, err := imapsync.Dial(ctx, account.IMAP, account.Email, opened.password)

			if err != nil {
				return fmt.Errorf("logging in: %w", err)
			}

			defer session.Close()

			var folderFailures []error

			lastSaved := time.Now()

			for _, planned := range plan.folders {
				folder := planned.folder
				uidValidity := planned.uidValidity
				fresh := planned.fresh

				if len(fresh) == 0 {
					continue
				}

				noun := "emails"
				if len(fresh) == 1 {
					noun = "email"
				}

				report("%s %s: downloading %d %s", account.Name, folder, len(fresh), noun)

				selectedValidity, err := session.Select(planned.server)

				if err != nil || selectedValidity != uidValidity {
					folderFailures = append(folderFailures, fmt.Errorf("%s changed during sync. Press Ctrl+R to try again", folder))
					continue
				}

				before, _ := b.SyncProgress()

				err = session.Fetch(fresh, func(batch []imapsync.Fetched) error {
					messages := make([]Message, len(batch))

					group, groupCtx := errgroup.WithContext(ctx)
					group.SetLimit(8)

					for i, fetched := range batch {
						group.Go(func() error {
							body := fetched.HTML

							if folder != Spam && body != "" {
								document, err := html.Parse(strings.NewReader(body))

								if err == nil {
									downloaded := map[string]string{}

									for node := range document.Descendants() {
										if node.Type != html.ElementNode || node.Data != "img" {
											continue
										}

										node.Attr = slices.DeleteFunc(node.Attr, func(attribute html.Attribute) bool { return attribute.Key == "srcset" })

										for a, attribute := range node.Attr {
											if attribute.Key != "src" || !(strings.HasPrefix(attribute.Val, "https://") || strings.HasPrefix(attribute.Val, "http://")) {
												continue
											}

											_, done := downloaded[attribute.Val]

											if !done {
												downloaded[attribute.Val] = ""

												request, err := http.NewRequestWithContext(groupCtx, http.MethodGet, attribute.Val, nil)

												if err != nil {
													continue
												}

												response, err := imageClient.Do(request)

												if err != nil {
													continue
												}

												image, err := io.ReadAll(io.LimitReader(response.Body, 5<<20+1))
												response.Body.Close()

												if err != nil || response.StatusCode != http.StatusOK || len(image) > 5<<20 {
													continue
												}

												contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))

												if !strings.HasPrefix(contentType, "image/") {
													contentType = http.DetectContentType(image)
												}

												if !strings.HasPrefix(contentType, "image/") {
													continue
												}

												downloaded[attribute.Val] = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(image)
											}

											if downloaded[attribute.Val] != "" {
												node.Attr[a].Val = downloaded[attribute.Val]
											}
										}
									}

									var rendered strings.Builder

									err = html.Render(&rendered, document)

									if err == nil {
										body = rendered.String()
									}
								}
							}

							id := fmt.Sprintf("%s:%s:%d:%d", account.Email, folder, uidValidity, fetched.UID)

							message := Message{
								ID:      id,
								Account: account.Name,
								Folder:  folder,
								From:    Address{Name: fetched.From.Name, Email: fetched.From.Email},
								Subject: fetched.Subject,
								Date:    fetched.Date,
								Unread:  !fetched.Seen,
								Text:    fetched.Text,
								UID:     fetched.UID,
							}

							for _, recipient := range fetched.To {
								message.To = append(message.To, Address{Name: recipient.Name, Email: recipient.Email})
							}

							stored := storedBody{HTML: body}

							for _, attachment := range fetched.Attachments {
								stored.Attachments = append(stored.Attachments, Attachment{Name: attachment.Name, ContentType: attachment.ContentType, Data: attachment.Data})
								message.Attachments = append(message.Attachments, Attachment{Name: attachment.Name, ContentType: attachment.ContentType})
							}

							var encoded bytes.Buffer

							err := gob.NewEncoder(&encoded).Encode(stored)

							if err != nil {
								return err
							}

							err = opened.mirror.Write(bodyFile(id), encoded.Bytes())

							if err != nil {
								return err
							}

							messages[i] = message

							return nil
						})
					}

					err := group.Wait()

					if err != nil {
						return err
					}

					b.mutex.Lock()
					b.messages = append(b.messages, messages...)
					b.syncDone += len(messages)
					b.mutex.Unlock()

					if time.Since(lastSaved) < 30*time.Second {
						return nil
					}

					lastSaved = time.Now()

					return b.saveIndex(account.Name, opened)
				})

				after, _ := b.SyncProgress()

				report("%s %s: downloaded %d of %d", account.Name, folder, after-before, len(fresh))

				if err != nil {
					folderFailures = append(folderFailures, fmt.Errorf("downloading %s: %w", folder, err))
				}
			}

			err = b.saveIndex(account.Name, opened)

			if err != nil {
				folderFailures = append(folderFailures, err)
			}

			return errors.Join(folderFailures...)
		}()

		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", account.Name, err))
		}
	}

	return errors.Join(failures...)
}

func (b *Mailbox) saveIndex(accountName string, opened *openedAccount) error {
	index := accountIndex{}

	b.mutex.Lock()

	index.Folders = maps.Clone(opened.folders)

	for _, message := range b.messages {
		if message.Account == accountName {
			index.Messages = append(index.Messages, message)
		}
	}

	b.mutex.Unlock()

	var encoded bytes.Buffer

	err := gob.NewEncoder(&encoded).Encode(index)

	if err != nil {
		return fmt.Errorf("saving the mirror index: %w", err)
	}

	err = opened.mirror.Write("index", encoded.Bytes())

	if err != nil {
		return fmt.Errorf("saving the mirror index: %w", err)
	}

	return nil
}

func bodyFile(id string) string {
	return fmt.Sprintf("messages/%x", sha256.Sum256([]byte(id)))[:9+32]
}

func (b *Mailbox) Send(draft Draft) error {
	return errors.New("sending isn't built yet")
}

package mailbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
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
	"net/smtp"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/emersion/go-message/mail"
	mimeheader "github.com/emersion/go-message/textproto"
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
	Outbox  = "Outbox"
)

const deleted = "" // the Folder of an email waiting for a sync to delete it from the server's Trash

var ErrAlreadyThere = errors.New("already in that folder")

var ErrInOutbox = errors.New("an email in the Outbox can't move until it is sent")

type Address struct {
	Name  string
	Email string
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
	ID           string
	Account      string
	Folder       string
	ServerFolder string
	From         Address
	To           []Address
	Cc           []Address
	Bcc          []Address
	Subject      string
	Date         time.Time
	MessageID    string
	References   []string
	Unread       bool
	Text         string
	HTML         string
	Attachments  []Attachment
	UID          uint32
}

type Account struct {
	Name                  string
	Email                 string
	IMAP                  string
	SMTP                  string
	Signature             string
	PasswordCommand       string
	Folders               []string
	HideFromCombinedInbox bool
}

type Draft struct {
	Account string
	To      string
	Cc      string
	Bcc     string
	Subject string
	Attach  []string
	Body    string
	ReplyTo string
}

type Mailbox struct {
	mutex           sync.Mutex
	saving          sync.Mutex
	accounts        []Account
	messages        []Message
	mirrorDirectory string
	historyDays     int
	opened          map[string]*openedAccount
	pendingSeen     map[string]bool
	progress        map[string]syncCount
	sending         map[string]bool
}

type syncCount struct {
	done  int
	total int
}

type openedAccount struct {
	password string
	mirror   *mirror.Mirror
	folders  map[string]folderIndex
	changed  bool
}

type folderIndex struct {
	Server      string
	UIDValidity uint32
}

type accountIndex struct {
	Folders      map[string]folderIndex
	Messages     []Message
	PendingReads []string
}

type storedBody struct {
	HTML        string
	Attachments []Attachment
	Raw         []byte
	BccHeader   []byte
	Recipients  []string
	Draft       Draft
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
		progress:        map[string]syncCount{},
		sending:         map[string]bool{},
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
		if message.Folder != folder || !b.viewIncludes(account, message) {
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

	return slices.DeleteFunc(slices.Clone(b.messages), func(m Message) bool { return m.Folder == deleted })
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
		if message.Unread && message.Folder == folder && b.viewIncludes(account, message) {
			count++
		}
	}

	return count
}

func (b *Mailbox) viewIncludes(account string, message Message) bool {
	if account != "" {
		return message.Account == account
	}

	return !slices.ContainsFunc(b.accounts, func(candidate Account) bool {
		return candidate.Name == message.Account && candidate.HideFromCombinedInbox
	})
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

	if message.Folder == Outbox {
		return ErrInOutbox
	}

	accountIndex := slices.IndexFunc(b.accounts, func(a Account) bool { return a.Name == message.Account })

	if accountIndex < 0 || !slices.Contains(b.accounts[accountIndex].Folders, folder) {
		return fmt.Errorf("%s has no %s folder", message.Account, folder)
	}

	message.Folder = folder
	b.opened[message.Account].changed = true

	return nil
}

func (b *Mailbox) MarkRead(id string) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for i := range b.messages {
		if b.messages[i].ID == id {
			b.messages[i].Unread = false
			b.pendingSeen[id] = true
			b.opened[b.messages[i].Account].changed = true
		}
	}
}

func (b *Mailbox) EmptyTrash(account string) int {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	count := 0

	for i := range b.messages {
		if b.messages[i].Account == account && b.messages[i].Folder == Trash {
			b.messages[i].Folder = deleted
			count++
		}
	}

	if count > 0 {
		b.opened[account].changed = true
	}

	return count
}

func (b *Mailbox) Save() error {
	b.mutex.Lock()

	changed := map[string]*openedAccount{}

	for name, opened := range b.opened {
		if opened.changed {
			changed[name] = opened
		}
	}

	b.mutex.Unlock()

	var failures []error

	for name, opened := range changed {
		err := b.saveIndex(name, opened)

		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
		}
	}

	return errors.Join(failures...)
}

func (b *Mailbox) Unlocked(account string) bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.opened[account] != nil
}

func (b *Mailbox) SyncProgress(account string) (done, total int) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	count := b.progress[account]

	return count.done, count.total
}

func (b *Mailbox) Sync(ctx context.Context, accountName string, report func(format string, arguments ...any)) error {
	account, ok := b.Account(accountName)

	if !ok {
		return fmt.Errorf("no account called %s", accountName)
	}

	b.mutex.Lock()
	b.progress[account.Name] = syncCount{}
	opened := b.opened[account.Name]
	b.mutex.Unlock()

	if opened == nil {
		words := strings.Fields(account.PasswordCommand)

		if len(words) == 0 {
			return errors.New("the config has no password-cmd")
		}

		report("Running password-cmd")

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
			report("The password doesn't open the mirror. Checking it against the server")

			session, loginErr := imapsync.Dial(ctx, account.IMAP, account.Email, password)

			if loginErr != nil {
				return fmt.Errorf("the password opens neither the mirror nor the server: %w", loginErr)
			}

			session.Close()

			report("The server accepted the password, so it has changed. Deleting the mirror to download it again")

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
			report("The mirror has no index yet, so this is a fresh mirror")

		case err != nil:
			return err

		default:
			err = gob.NewDecoder(bytes.NewReader(content)).Decode(&index)

			if err != nil {
				return fmt.Errorf("reading the mirror index: %w", err)
			}

			report("Opened the mirror: %d emails and %d pending reads", len(index.Messages), len(index.PendingReads))
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

		for _, id := range index.PendingReads {
			b.pendingSeen[id] = true
		}

		b.opened[account.Name] = opened
		b.mutex.Unlock()
	}

	report("Logging in to %s", account.IMAP)

	session, err := imapsync.Dial(ctx, account.IMAP, account.Email, opened.password)

	if err != nil {
		return fmt.Errorf("logging in: %w", err)
	}

	defer session.Close()

	report("Logged in")

	serverFolders, err := session.Folders()

	if err != nil {
		return fmt.Errorf("listing folders: %w", err)
	}

	var failures []error

	servers := map[string]string{}

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
			report("%s: the server has no matching folder", folder)
			failures = append(failures, fmt.Errorf("the server has no %s folder", folder))
			continue
		}

		report("%s: the server calls it %s", folder, server)

		servers[folder] = server
	}

	var outbox []Message

	b.mutex.Lock()

	for _, message := range b.messages {
		if message.Account == account.Name && message.Folder == Outbox {
			outbox = append(outbox, message)
		}
	}

	b.mutex.Unlock()

	removeQueued := func(id string) {
		b.mutex.Lock()
		b.messages = slices.DeleteFunc(b.messages, func(m Message) bool { return m.ID == id })
		b.mutex.Unlock()

		opened.mirror.Remove(bodyFile(id))
	}

	switch {
	case len(outbox) == 0:
		report("Outbox: nothing to send")

	case servers[Sent] == "":
		report("Outbox: %d to send. The server has no Sent folder, so scry can't check whether an earlier try already sent them", len(outbox))

	default:
		report("Outbox: %d to send", len(outbox))

		_, err := session.Select(servers[Sent])

		if err != nil {
			report("Outbox: opening Sent to check for earlier tries failed, so nothing is sent this time: %v", err)
			failures = append(failures, fmt.Errorf("opening Sent: %w", err))
			outbox = nil
		}
	}

	for _, queued := range outbox {
		if servers[Sent] != "" {
			alreadySent, err := session.HasMessageID(queued.MessageID)

			if err != nil {
				report("Outbox: checking Sent failed, so the rest wait for the next sync: %v", err)
				failures = append(failures, fmt.Errorf("checking Sent: %w", err))
				break
			}

			if alreadySent {
				report("Outbox: an email is already in Sent, so an earlier try sent it. Removing it from the outbox")
				removeQueued(queued.ID)
				continue
			}
		}

		content, err := opened.mirror.Read(bodyFile(queued.ID))

		var stored storedBody

		if err == nil {
			err = gob.NewDecoder(bytes.NewReader(content)).Decode(&stored)
		}

		if err != nil {
			report("Outbox: reading a queued email from the mirror failed: %v", err)
			failures = append(failures, fmt.Errorf("reading a queued email: %w", err))
			continue
		}

		b.mutex.Lock()

		stillQueued := slices.ContainsFunc(b.messages, func(m Message) bool { return m.ID == queued.ID })

		if stillQueued {
			b.sending[queued.ID] = true
		}

		b.mutex.Unlock()

		if !stillQueued {
			report("Outbox: an email went back to the draft before it was sent")
			continue
		}

		report("Outbox: sending an email to %d recipients through %s", len(stored.Recipients), account.SMTP)

		err = func() error {
			// SMTP replies can quote addresses, which the sync log must never record.
			serverError := func(step string, err error) error {
				var reply *textproto.Error

				if errors.As(err, &reply) {
					return fmt.Errorf("%s: the server replied with code %d", step, reply.Code)
				}

				return fmt.Errorf("%s: %w", step, err)
			}

			host, port, err := net.SplitHostPort(account.SMTP)

			if err != nil {
				return fmt.Errorf("reading the smtp address: %w", err)
			}

			tlsConfig := &tls.Config{ServerName: host}
			dialer := &net.Dialer{Timeout: 30 * time.Second}

			var connection net.Conn

			if port == "465" {
				connection, err = tls.DialWithDialer(dialer, "tcp", account.SMTP, tlsConfig)
			} else {
				connection, err = dialer.Dial("tcp", account.SMTP)
			}

			if err != nil {
				return fmt.Errorf("connecting to the smtp server: %w", err)
			}

			connection.SetDeadline(time.Now().Add(time.Minute))

			client, err := smtp.NewClient(connection, host)

			if err != nil {
				connection.Close()
				return serverError("connecting to the smtp server", err)
			}

			defer client.Close()

			if port != "465" {
				connection.SetDeadline(time.Now().Add(time.Minute))

				err = client.StartTLS(tlsConfig)

				if err != nil {
					return serverError("starting TLS", err)
				}
			}

			connection.SetDeadline(time.Now().Add(time.Minute))

			err = client.Auth(smtp.PlainAuth("", account.Email, opened.password, host))

			if err != nil {
				return serverError("signing in", err)
			}

			connection.SetDeadline(time.Now().Add(time.Minute))

			err = client.Mail(account.Email)

			if err != nil {
				return serverError("starting the email", err)
			}

			for i, recipient := range stored.Recipients {
				connection.SetDeadline(time.Now().Add(time.Minute))

				err = client.Rcpt(recipient)

				if err != nil {
					return serverError(fmt.Sprintf("recipient %d of %d", i+1, len(stored.Recipients)), err)
				}
			}

			connection.SetDeadline(time.Now().Add(time.Minute))

			data, err := client.Data()

			if err != nil {
				return serverError("starting the data", err)
			}

			for chunk := range slices.Chunk(stored.Raw, 64<<10) {
				connection.SetDeadline(time.Now().Add(time.Minute))

				_, err = data.Write(chunk)

				if err != nil {
					return serverError("sending the data", err)
				}
			}

			connection.SetDeadline(time.Now().Add(time.Minute))

			err = data.Close()

			if err != nil {
				return serverError("finishing the email", err)
			}

			// The server has accepted the email once DATA ends, so a failed QUIT must not report a failed send.
			connection.SetDeadline(time.Now().Add(time.Minute))
			client.Quit()

			return nil
		}()

		b.mutex.Lock()
		delete(b.sending, queued.ID)
		b.mutex.Unlock()

		if err != nil {
			report("Outbox: sending failed, so the email waits for the next sync: %v", err)
			failures = append(failures, fmt.Errorf("sending: %w", err))
			continue
		}

		report("Outbox: sent")
		removeQueued(queued.ID)

		if servers[Sent] == "" {
			continue
		}

		filed, err := session.HasMessageID(queued.MessageID)

		if err != nil {
			report("Outbox: checking Sent for the server's own copy failed, so no copy is saved: %v", err)
			failures = append(failures, fmt.Errorf("checking Sent: %w", err))
			continue
		}

		if filed {
			report("Outbox: the server saved its own copy in Sent")
			continue
		}

		err = session.AppendSeen(servers[Sent], append(stored.BccHeader, stored.Raw...))

		if err != nil {
			report("Outbox: saving a copy to Sent failed: %v", err)
			failures = append(failures, fmt.Errorf("saving a copy to Sent: %w", err))
			continue
		}

		report("Outbox: saved a copy to Sent")
	}

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
						return errPrivateAddress
					}

					return nil
				},
			}).DialContext,
			ForceAttemptHTTP2:   true,
			MaxIdleConnsPerHost: 4,
		},
	}

	pendingMoves := map[string][]Message{}

	b.mutex.Lock()

	for _, message := range b.messages {
		if message.Account == account.Name && message.Folder != message.ServerFolder && !(message.Folder == deleted && message.ServerFolder == Trash) {
			pendingMoves[message.ServerFolder] = append(pendingMoves[message.ServerFolder], message)
		}
	}

	b.mutex.Unlock()

	if len(pendingMoves) == 0 {
		report("Moves: nothing to move")
	}

	for _, source := range account.Folders {
		moves := pendingMoves[source]
		sourceServer := servers[source]

		if len(moves) == 0 {
			continue
		}

		if sourceServer == "" {
			report("%s: %d moves wait, because the server has no %s folder", source, len(moves), source)
			continue
		}

		uidValidity, err := session.Select(sourceServer)

		if err != nil {
			report("%s: opening the folder to move %d emails failed: %v", source, len(moves), err)
			failures = append(failures, fmt.Errorf("opening %s: %w", source, err))
			continue
		}

		var uids []uint32

		for _, message := range moves {
			uids = append(uids, message.UID)
		}

		present := map[uint32]bool{}
		known := opened.folders[source]

		if known.Server == sourceServer && known.UIDValidity == uidValidity {
			present, err = session.Seen(uids)

			if err != nil {
				report("%s: checking %d emails to move failed: %v", source, len(moves), err)
				failures = append(failures, fmt.Errorf("checking emails to move from %s: %w", source, err))
				continue
			}
		} else {
			report("%s: the folder was rebuilt on the server, so its %d moves can't be matched", source, len(moves))
		}

		dropped := map[string]bool{}
		byDestination := map[string][]uint32{}

		for _, message := range moves {
			_, onServer := present[message.UID]

			if !onServer {
				dropped[message.ID] = true
				continue
			}

			destination := message.Folder

			if destination == deleted {
				destination = Trash
			}

			byDestination[destination] = append(byDestination[destination], message.UID)
		}

		b.mutex.Lock()

		b.messages = slices.DeleteFunc(b.messages, func(m Message) bool { return dropped[m.ID] })

		for id := range dropped {
			delete(b.pendingSeen, id)
		}

		b.mutex.Unlock()

		for id := range dropped {
			opened.mirror.Remove(bodyFile(id))
		}

		movedCount := 0
		var leftBlankFolders []string

		for _, destination := range account.Folders {
			destinationUIDs := byDestination[destination]

			if len(destinationUIDs) == 0 {
				continue
			}

			if servers[destination] == "" {
				report("%s: %d moves to %s wait, because the server has no %s folder", source, len(destinationUIDs), destination, destination)
				continue
			}

			moved, err := session.Move(destinationUIDs, servers[destination])

			if err != nil {
				report("%s: moving %d emails to %s failed: %v", source, len(destinationUIDs), destination, err)
				failures = append(failures, fmt.Errorf("moving email from %s to %s: %w", source, destination, err))
				continue
			}

			if len(moved) < len(destinationUIDs) {
				report("%s: the server confirmed %d of %d moves to %s. The rest stay pending", source, len(moved), len(destinationUIDs), destination)
			}

			b.mutex.Lock()

			for i, message := range b.messages {
				newUID, ok := moved[message.UID]

				if ok && message.Account == account.Name && message.ServerFolder == source {
					b.messages[i].ServerFolder = destination
					b.messages[i].UID = newUID

					if (source == Spam || source == Trash) && destination != Spam && destination != Trash {
						leftBlankFolders = append(leftBlankFolders, message.ID)
					}
				}
			}

			b.mutex.Unlock()

			movedCount += len(moved)
		}

		report("%s: moved %d, dropped %d already moved or deleted elsewhere", source, movedCount, len(dropped))

		imageOutcomes := map[string]int{}

		for _, id := range leftBlankFolders {
			content, err := opened.mirror.Read(bodyFile(id))

			var stored storedBody

			if err == nil {
				err = gob.NewDecoder(bytes.NewReader(content)).Decode(&stored)
			}

			if err != nil {
				report("%s: reading an email moved out of %s failed: %v", source, source, err)
				failures = append(failures, fmt.Errorf("reading an email moved out of %s: %w", source, err))
				continue
			}

			if stored.HTML == "" {
				continue
			}

			stored.HTML = fetchImages(ctx, imageClient, stored.HTML, func(outcome string) { imageOutcomes[outcome]++ })

			var encoded bytes.Buffer

			err = gob.NewEncoder(&encoded).Encode(stored)

			if err == nil {
				err = opened.mirror.Write(bodyFile(id), encoded.Bytes())
			}

			if err != nil {
				report("%s: saving the images of an email moved out of %s failed: %v", source, source, err)
				failures = append(failures, fmt.Errorf("saving an email moved out of %s: %w", source, err))
			}
		}

		if len(imageOutcomes) > 0 {
			report("%s: remote images in %d emails moved out: %s", source, len(leftBlankFolders), imageReport(imageOutcomes))
		}
	}

	var deleteIDs []string
	var deleteUIDs []uint32

	b.mutex.Lock()

	for _, message := range b.messages {
		if message.Account == account.Name && message.Folder == deleted && message.ServerFolder == Trash {
			deleteIDs = append(deleteIDs, message.ID)
			deleteUIDs = append(deleteUIDs, message.UID)
		}
	}

	b.mutex.Unlock()

	switch {
	case len(deleteIDs) == 0:
		report("Trash: nothing to delete")

	case servers[Trash] == "":
		report("Trash: %d deletions wait, because the server has no Trash folder", len(deleteIDs))

	default:
		err := func() error {
			uidValidity, err := session.Select(servers[Trash])

			if err != nil {
				return fmt.Errorf("opening Trash to delete %d emails: %w", len(deleteIDs), err)
			}

			present := map[uint32]bool{}
			known := opened.folders[Trash]

			if known.Server == servers[Trash] && known.UIDValidity == uidValidity {
				present, err = session.Seen(deleteUIDs)

				if err != nil {
					return fmt.Errorf("checking %d emails to delete in Trash: %w", len(deleteIDs), err)
				}
			} else {
				report("Trash: the folder was rebuilt on the server, so its %d deletions can't be matched", len(deleteIDs))
			}

			var presentUIDs []uint32

			for _, uid := range deleteUIDs {
				_, onServer := present[uid]

				if onServer {
					presentUIDs = append(presentUIDs, uid)
				}
			}

			if len(presentUIDs) > 0 {
				err = session.Delete(presentUIDs)

				if err != nil {
					return fmt.Errorf("deleting %d emails from Trash: %w", len(presentUIDs), err)
				}
			}

			b.mutex.Lock()

			b.messages = slices.DeleteFunc(b.messages, func(m Message) bool { return slices.Contains(deleteIDs, m.ID) })

			for _, id := range deleteIDs {
				delete(b.pendingSeen, id)
			}

			b.mutex.Unlock()

			for _, id := range deleteIDs {
				opened.mirror.Remove(bodyFile(id))
			}

			report("Trash: deleted %d for good, dropped %d already deleted elsewhere", len(presentUIDs), len(deleteIDs)-len(presentUIDs))

			return nil
		}()

		if err != nil {
			report("Trash: %v", err)
			failures = append(failures, err)
		}
	}

	pushedReads := 0

	for _, folder := range account.Folders {
		server, ok := servers[folder]

		if !ok {
			continue
		}

		var pendingIDs []string
		var pendingUIDs []uint32

		b.mutex.Lock()

		for _, message := range b.messages {
			if message.Account == account.Name && message.ServerFolder == folder && b.pendingSeen[message.ID] {
				pendingIDs = append(pendingIDs, message.ID)
				pendingUIDs = append(pendingUIDs, message.UID)
			}
		}

		b.mutex.Unlock()

		if len(pendingIDs) == 0 {
			continue
		}

		uidValidity, err := session.Select(server)

		if err != nil {
			report("%s: opening the folder to mark %d read failed: %v", folder, len(pendingIDs), err)
			failures = append(failures, fmt.Errorf("opening %s: %w", folder, err))
			continue
		}

		present := map[uint32]bool{}
		known := opened.folders[folder]

		if known.Server == server && known.UIDValidity == uidValidity {
			present, err = session.Seen(pendingUIDs)

			if err != nil {
				report("%s: checking %d emails to mark read failed: %v", folder, len(pendingIDs), err)
				failures = append(failures, fmt.Errorf("checking emails to mark read in %s: %w", folder, err))
				continue
			}
		} else {
			report("%s: the folder was rebuilt on the server, so its %d pending reads can't be matched", folder, len(pendingIDs))
		}

		var presentIDs []string
		var presentUIDs []uint32
		dropped := 0

		b.mutex.Lock()

		for i, id := range pendingIDs {
			_, onServer := present[pendingUIDs[i]]

			if !onServer {
				delete(b.pendingSeen, id)
				dropped++
				continue
			}

			presentIDs = append(presentIDs, id)
			presentUIDs = append(presentUIDs, pendingUIDs[i])
		}

		b.mutex.Unlock()

		if len(presentUIDs) > 0 {
			err = session.MarkSeen(presentUIDs)

			if err != nil {
				report("%s: marking %d read failed: %v", folder, len(presentUIDs), err)
				failures = append(failures, fmt.Errorf("marking email read in %s: %w", folder, err))
				continue
			}
		}

		b.mutex.Lock()

		for _, id := range presentIDs {
			delete(b.pendingSeen, id)
		}

		b.mutex.Unlock()

		report("%s: marked %d read on the server, dropped %d already moved or deleted elsewhere", folder, len(presentIDs), dropped)

		pushedReads += len(presentIDs)
	}

	if pushedReads == 0 {
		report("Reads: nothing to mark read")
	}

	type plannedFolder struct {
		folder      string
		server      string
		uidValidity uint32
		fresh       []uint32
	}

	var planned []plannedFolder

	for _, folder := range account.Folders {
		server, ok := servers[folder]

		if !ok {
			continue
		}

		uidValidity, err := session.Select(server)

		if err != nil {
			report("%s: opening the folder failed: %v", folder, err)
			failures = append(failures, fmt.Errorf("opening %s: %w", folder, err))
			continue
		}

		known := opened.folders[folder]

		if known.Server != server || known.UIDValidity != uidValidity {
			var dropped []string

			b.mutex.Lock()

			b.messages = slices.DeleteFunc(b.messages, func(m Message) bool {
				drop := m.Account == account.Name && m.ServerFolder == folder

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

			if known.Server == "" {
				report("%s: first sync of this folder", folder)
			} else {
				report("%s: the folder was rebuilt on the server, so its %d local copies are dropped and download again", folder, len(dropped))
			}
		}

		since := time.Time{}

		if folder == Archive || folder == Sent {
			since = time.Now().AddDate(0, 0, -b.historyDays)
		}

		uids, err := session.Search(since)

		if err != nil {
			report("%s: searching failed: %v", folder, err)
			failures = append(failures, fmt.Errorf("searching %s: %w", folder, err))
			continue
		}

		heldNoun := "emails"
		if len(uids) == 1 {
			heldNoun = "email"
		}

		if since.IsZero() {
			report("%s: the server holds %d %s", folder, len(uids), heldNoun)
		} else {
			report("%s: the server holds %d %s from the last %d days", folder, len(uids), heldNoun, b.historyDays)
		}

		seen, err := session.Seen(uids)

		if err != nil {
			report("%s: reading flags failed: %v", folder, err)
			failures = append(failures, fmt.Errorf("reading flags in %s: %w", folder, err))
			continue
		}

		knownUIDs := map[uint32]bool{}
		var removed []string
		readChanges := 0
		heldReads := 0

		b.mutex.Lock()

		for _, message := range b.messages {
			if message.Account == account.Name && message.ServerFolder == folder {
				knownUIDs[message.UID] = true
			}
		}

		b.messages = slices.DeleteFunc(b.messages, func(m Message) bool {
			_, onServer := seen[m.UID]
			remove := m.Account == account.Name && m.ServerFolder == folder && !onServer

			if remove {
				removed = append(removed, m.ID)
			}

			return remove
		})

		for i, message := range b.messages {
			if message.Account != account.Name || message.ServerFolder != folder {
				continue
			}

			if b.pendingSeen[message.ID] {
				heldReads++
				continue
			}

			unread := !seen[message.UID]

			if message.Unread != unread {
				readChanges++
			}

			b.messages[i].Unread = unread
		}

		b.mutex.Unlock()

		for _, id := range removed {
			opened.mirror.Remove(bodyFile(id))
		}

		var fresh []uint32

		for _, uid := range uids {
			if !knownUIDs[uid] {
				fresh = append(fresh, uid)
			}
		}

		report("%s: %d removed elsewhere, %d changed read state elsewhere, %d new", folder, len(removed), readChanges, len(fresh))

		if heldReads > 0 {
			report("%s: kept %d local reads that haven't reached the server yet", folder, heldReads)
		}

		planned = append(planned, plannedFolder{folder: folder, server: server, uidValidity: uidValidity, fresh: fresh})
	}

	total := 0

	for _, folder := range planned {
		total += len(folder.fresh)
	}

	b.mutex.Lock()
	b.progress[account.Name] = syncCount{total: total}
	b.mutex.Unlock()

	err = b.saveIndex(account.Name, opened)

	if err != nil {
		report("Saving the mirror index failed: %v", err)
		failures = append(failures, err)
	}

	if total == 0 {
		report("Downloads: nothing new")
	}

	lastSaved := time.Now()

	for _, planned := range planned {
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

		report("%s: downloading %d %s", folder, len(fresh), noun)

		selectedValidity, err := session.Select(planned.server)

		if err != nil {
			report("%s: opening the folder to download failed: %v", folder, err)
			failures = append(failures, fmt.Errorf("opening %s: %w", folder, err))
			continue
		}

		if selectedValidity != uidValidity {
			report("%s: the folder changed during sync, so its downloads wait for the next sync", folder)
			failures = append(failures, fmt.Errorf("%s changed during sync", folder))
			continue
		}

		before, _ := b.SyncProgress(account.Name)

		err = session.Fetch(fresh, func(batch []imapsync.Fetched) error {
			messages := make([]Message, len(batch))

			var imageMutex sync.Mutex
			imageOutcomes := map[string]int{}

			countImage := func(outcome string) {
				imageMutex.Lock()
				imageOutcomes[outcome]++
				imageMutex.Unlock()
			}

			group, groupCtx := errgroup.WithContext(ctx)
			group.SetLimit(8)

			for i, fetched := range batch {
				group.Go(func() error {
					body := fetched.HTML

					switch {
					case body == "":

					case folder == Spam || folder == Trash:
						countImage("skipped in " + folder)

					default:
						body = fetchImages(groupCtx, imageClient, body, countImage)
					}

					id := fmt.Sprintf("%s:%s:%d:%d", account.Email, folder, uidValidity, fetched.UID)

					message := Message{
						ID:           id,
						Account:      account.Name,
						Folder:       folder,
						ServerFolder: folder,
						From:         Address{Name: fetched.From.Name, Email: fetched.From.Email},
						Subject:      fetched.Subject,
						Date:         fetched.Date,
						MessageID:    fetched.MessageID,
						References:   fetched.References,
						Unread:       !fetched.Seen,
						Text:         fetched.Text,
						UID:          fetched.UID,
					}

					for _, recipient := range fetched.To {
						message.To = append(message.To, Address{Name: recipient.Name, Email: recipient.Email})
					}

					for _, recipient := range fetched.Cc {
						message.Cc = append(message.Cc, Address{Name: recipient.Name, Email: recipient.Email})
					}

					for _, recipient := range fetched.Bcc {
						message.Bcc = append(message.Bcc, Address{Name: recipient.Name, Email: recipient.Email})
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
				report("%s: saving a batch of %d failed: %v", folder, len(batch), err)
				return err
			}

			b.mutex.Lock()
			b.messages = append(b.messages, messages...)
			count := b.progress[account.Name]
			count.done += len(messages)
			b.progress[account.Name] = count
			b.mutex.Unlock()

			report("%s: saved %d emails, %d of %d this sync", folder, len(messages), count.done, count.total)

			if len(imageOutcomes) > 0 {
				report("%s: remote images: %s", folder, imageReport(imageOutcomes))
			}

			if time.Since(lastSaved) < 30*time.Second {
				return nil
			}

			lastSaved = time.Now()

			report("Saving the mirror index mid-download")

			return b.saveIndex(account.Name, opened)
		})

		after, _ := b.SyncProgress(account.Name)

		report("%s: downloaded %d of %d", folder, after-before, len(fresh))

		if err != nil {
			report("%s: downloading stopped: %v", folder, err)
			failures = append(failures, fmt.Errorf("downloading %s: %w", folder, err))
		}
	}

	err = b.saveIndex(account.Name, opened)

	if err != nil {
		report("Saving the mirror index failed: %v", err)
		failures = append(failures, err)
	}

	return errors.Join(failures...)
}

func (b *Mailbox) Unqueue(id string) (Draft, error) {
	message, ok := b.Get(id)

	if !ok || message.Folder != Outbox {
		return Draft{}, errors.New("this email isn't in the Outbox any more")
	}

	b.mutex.Lock()
	opened := b.opened[message.Account]
	b.mutex.Unlock()

	content, err := opened.mirror.Read(bodyFile(id))

	if err != nil {
		return Draft{}, err
	}

	var stored storedBody

	err = gob.NewDecoder(bytes.NewReader(content)).Decode(&stored)

	if err != nil {
		return Draft{}, err
	}

	b.mutex.Lock()

	if b.sending[id] {
		b.mutex.Unlock()
		return Draft{}, errors.New("this email is being sent right now")
	}

	b.messages = slices.DeleteFunc(b.messages, func(m Message) bool { return m.ID == id })

	b.mutex.Unlock()

	opened.mirror.Remove(bodyFile(id))

	return stored.Draft, b.saveIndex(message.Account, opened)
}

func (b *Mailbox) WatchInbox(ctx context.Context, accountName string, report func(format string, arguments ...any)) error {
	account, ok := b.Account(accountName)

	if !ok {
		return fmt.Errorf("no account called %s", accountName)
	}

	b.mutex.Lock()
	opened := b.opened[account.Name]
	b.mutex.Unlock()

	if opened == nil {
		return errors.New("the account isn't unlocked yet")
	}

	session, err := imapsync.Dial(ctx, account.IMAP, account.Email, opened.password)

	if err != nil {
		return fmt.Errorf("logging in: %w", err)
	}

	defer session.Close()

	_, err = session.Select("INBOX")

	if err != nil {
		return fmt.Errorf("opening Inbox: %w", err)
	}

	report("Inbox watch: waiting for the server to report new mail")

	for {
		changed, err := session.Idle(ctx, 10*time.Minute)

		if err != nil {
			return err
		}

		if changed {
			report("Inbox watch: the server reported a change")
			return nil
		}

		report("Inbox watch: renewing after 10 quiet minutes")
	}
}

func (b *Mailbox) saveIndex(accountName string, opened *openedAccount) error {
	b.saving.Lock()
	defer b.saving.Unlock()

	index := accountIndex{}

	b.mutex.Lock()

	index.Folders = maps.Clone(opened.folders)

	for _, message := range b.messages {
		if message.Account != accountName {
			continue
		}

		index.Messages = append(index.Messages, message)

		if b.pendingSeen[message.ID] {
			index.PendingReads = append(index.PendingReads, message.ID)
		}
	}

	opened.changed = false

	b.mutex.Unlock()

	var encoded bytes.Buffer

	err := gob.NewEncoder(&encoded).Encode(index)

	if err == nil {
		err = opened.mirror.Write("index", encoded.Bytes())
	}

	if err != nil {
		b.mutex.Lock()
		opened.changed = true
		b.mutex.Unlock()

		return fmt.Errorf("saving the mirror index: %w", err)
	}

	return nil
}

func bodyFile(id string) string {
	return fmt.Sprintf("messages/%x", sha256.Sum256([]byte(id)))[:9+32]
}

func (b *Mailbox) Queue(draft Draft) error {
	account, ok := b.Account(draft.Account)

	if !ok {
		return fmt.Errorf("no account called %s", draft.Account)
	}

	b.mutex.Lock()
	opened := b.opened[account.Name]
	b.mutex.Unlock()

	if opened == nil {
		return errors.New("scry hasn't unlocked this account yet. Press Ctrl+R to sync it")
	}

	recipients, err := mail.ParseAddressList(draft.To)

	if err != nil {
		return fmt.Errorf("reading the To line: %w", err)
	}

	var cc, bcc []*mail.Address

	if strings.TrimSpace(draft.Cc) != "" {
		cc, err = mail.ParseAddressList(draft.Cc)

		if err != nil {
			return fmt.Errorf("reading the Cc line: %w", err)
		}
	}

	if strings.TrimSpace(draft.Bcc) != "" {
		bcc, err = mail.ParseAddressList(draft.Bcc)

		if err != nil {
			return fmt.Errorf("reading the Bcc line: %w", err)
		}
	}

	_, domain, _ := strings.Cut(account.Email, "@")

	now := time.Now()

	var header mail.Header

	header.SetDate(now)
	header.SetAddressList("From", []*mail.Address{{Address: account.Email}})
	header.SetAddressList("To", recipients)
	header.SetAddressList("Cc", cc)
	header.SetSubject(draft.Subject)

	err = header.GenerateMessageIDWithHostname(domain)

	if err != nil {
		return err
	}

	messageID, err := header.MessageID()

	if err != nil {
		return err
	}

	original, ok := b.Get(draft.ReplyTo)

	if ok && original.MessageID != "" {
		header.SetMsgIDList("In-Reply-To", []string{original.MessageID})
		header.SetMsgIDList("References", append(slices.Clone(original.References), original.MessageID))
	}

	var rendered strings.Builder

	block := ""

	for _, line := range strings.Split(draft.Body, "\n") {
		item, isItem := strings.CutPrefix(line, "- ")

		kind := "p"
		text := line

		switch {
		case strings.TrimSpace(line) == "":
			kind = ""

		case isItem:
			kind = "ul"
			text = item
		}

		if kind != block && block != "" {
			rendered.WriteString("</" + block + ">")
		}

		if kind != block && kind != "" {
			rendered.WriteString("<" + kind + ">")
		}

		if kind == block && kind == "p" {
			rendered.WriteString("<br>")
		}

		block = kind

		if kind == "" {
			continue
		}

		parts := strings.Split(html.EscapeString(text), "**")

		var inline strings.Builder

		for i, part := range parts {
			switch {
			case i == 0:

			case i%2 == 1 && i == len(parts)-1:
				inline.WriteString("**")

			case i%2 == 1:
				inline.WriteString("<strong>")

			default:
				inline.WriteString("</strong>")
			}

			inline.WriteString(part)
		}

		if kind == "ul" {
			rendered.WriteString("<li>" + inline.String() + "</li>")
			continue
		}

		rendered.WriteString(inline.String())
	}

	if block != "" {
		rendered.WriteString("</" + block + ">")
	}

	plainText := draft.Body
	htmlText := rendered.String()

	if account.Signature != "" {
		signature, err := os.ReadFile(account.Signature)

		if err != nil {
			return fmt.Errorf("reading the signature: %w", err)
		}

		plainText += "\n\n" + imapsync.PlainText(string(signature))
		htmlText += string(signature)
	}

	var encoded bytes.Buffer

	writer, err := mail.CreateWriter(&encoded, header)

	if err != nil {
		return err
	}

	alternatives, err := writer.CreateInline()

	if err != nil {
		return err
	}

	for _, part := range []struct{ contentType, content string }{{"text/plain", plainText}, {"text/html", htmlText}} {
		var partHeader mail.InlineHeader

		partHeader.SetContentType(part.contentType, map[string]string{"charset": "utf-8"})

		partWriter, err := alternatives.CreatePart(partHeader)

		if err != nil {
			return err
		}

		_, err = io.WriteString(partWriter, part.content)

		if err != nil {
			return err
		}

		err = partWriter.Close()

		if err != nil {
			return err
		}
	}

	err = alternatives.Close()

	if err != nil {
		return err
	}

	var attachments []Attachment

	for _, path := range draft.Attach {
		content, err := os.ReadFile(path)

		if err != nil {
			return fmt.Errorf("reading the attachment %s: %w", filepath.Base(path), err)
		}

		contentType := mime.TypeByExtension(filepath.Ext(path))

		if contentType == "" {
			contentType = "application/octet-stream"
		}

		attachments = append(attachments, Attachment{Name: filepath.Base(path), ContentType: contentType, Data: content})

		var attachmentHeader mail.AttachmentHeader

		attachmentHeader.Set("Content-Type", contentType)
		attachmentHeader.SetFilename(filepath.Base(path))

		attachmentWriter, err := writer.CreateAttachment(attachmentHeader)

		if err != nil {
			return err
		}

		_, err = attachmentWriter.Write(content)

		if err != nil {
			return err
		}

		err = attachmentWriter.Close()

		if err != nil {
			return err
		}
	}

	err = writer.Close()

	if err != nil {
		return err
	}

	id := "outbox:" + messageID

	message := Message{
		ID:           id,
		Account:      account.Name,
		Folder:       Outbox,
		ServerFolder: Outbox,
		From:         Address{Email: account.Email},
		Subject:      draft.Subject,
		Date:         now,
		MessageID:    messageID,
		Text:         plainText,
	}

	// Only the copy in Sent carries the Bcc line, so the recipients never see it.
	var bccFields mail.Header

	bccFields.SetAddressList("Bcc", bcc)

	var bccHeader bytes.Buffer

	err = mimeheader.WriteHeader(&bccHeader, bccFields.Header.Header)

	if err != nil {
		return err
	}

	stored := storedBody{HTML: htmlText, Attachments: attachments, Raw: encoded.Bytes(), BccHeader: bytes.TrimSuffix(bccHeader.Bytes(), []byte("\r\n")), Draft: draft}

	for _, recipient := range recipients {
		message.To = append(message.To, Address{Name: recipient.Name, Email: recipient.Address})
		stored.Recipients = append(stored.Recipients, recipient.Address)
	}

	for _, recipient := range cc {
		message.Cc = append(message.Cc, Address{Name: recipient.Name, Email: recipient.Address})
		stored.Recipients = append(stored.Recipients, recipient.Address)
	}

	for _, recipient := range bcc {
		message.Bcc = append(message.Bcc, Address{Name: recipient.Name, Email: recipient.Address})
		stored.Recipients = append(stored.Recipients, recipient.Address)
	}

	for _, attachment := range attachments {
		message.Attachments = append(message.Attachments, Attachment{Name: attachment.Name, ContentType: attachment.ContentType})
	}

	var storedEncoded bytes.Buffer

	err = gob.NewEncoder(&storedEncoded).Encode(stored)

	if err != nil {
		return err
	}

	err = opened.mirror.Write(bodyFile(id), storedEncoded.Bytes())

	if err != nil {
		return fmt.Errorf("saving to the outbox: %w", err)
	}

	b.mutex.Lock()
	b.messages = append(b.messages, message)
	b.mutex.Unlock()

	return b.saveIndex(account.Name, opened)
}

var errPrivateAddress = errors.New("refusing to fetch an image from a private address")

func fetchImages(ctx context.Context, client *http.Client, body string, countImage func(outcome string)) string {
	document, err := html.Parse(strings.NewReader(body))

	if err != nil {
		countImage("skipped in unreadable HTML")
		return body
	}

	downloaded := map[string]string{}

	for node := range document.Descendants() {
		if node.Type != html.ElementNode {
			continue
		}

		node.Attr = slices.DeleteFunc(node.Attr, func(attribute html.Attribute) bool { return attribute.Key == "srcset" })

		for a, attribute := range node.Attr {
			isImage := (node.Data == "img" && attribute.Key == "src") || attribute.Key == "background"

			if !isImage || !(strings.HasPrefix(attribute.Val, "https://") || strings.HasPrefix(attribute.Val, "http://")) {
				continue
			}

			_, done := downloaded[attribute.Val]

			if !done {
				downloaded[attribute.Val] = ""

				request, err := http.NewRequestWithContext(ctx, http.MethodGet, attribute.Val, nil)

				if err != nil {
					countImage("unusable address")
					continue
				}

				response, err := client.Do(request)

				if errors.Is(err, errPrivateAddress) {
					countImage("refused private address")
					continue
				}

				if err != nil {
					countImage("unreachable")
					continue
				}

				image, err := io.ReadAll(io.LimitReader(response.Body, 5<<20+1))
				response.Body.Close()

				switch {
				case err != nil:
					countImage("interrupted")
					continue

				case response.StatusCode != http.StatusOK:
					countImage("refused by the image server")
					continue

				case len(image) > 5<<20:
					countImage("over 5 MB")
					continue
				}

				contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))

				if !strings.HasPrefix(contentType, "image/") {
					contentType = http.DetectContentType(image)
				}

				if !strings.HasPrefix(contentType, "image/") {
					countImage("not an image")
					continue
				}

				countImage("saved")

				downloaded[attribute.Val] = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(image)
			}

			if downloaded[attribute.Val] != "" {
				node.Attr[a].Val = downloaded[attribute.Val]
			}
		}
	}

	var rendered strings.Builder

	err = html.Render(&rendered, document)

	if err != nil {
		return body
	}

	return rendered.String()
}

func imageReport(outcomes map[string]int) string {
	var parts []string

	for outcome, number := range outcomes {
		parts = append(parts, fmt.Sprintf("%d %s", number, outcome))
	}

	slices.Sort(parts)

	return strings.Join(parts, ", ")
}

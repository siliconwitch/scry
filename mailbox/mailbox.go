package mailbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
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
}

type Account struct {
	Name    string
	Email   string
	Folders []string
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
	mutex    sync.Mutex
	accounts []Account
	messages []Message
	nextID   int
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

	message.Folder = folder

	return nil
}

func (b *Mailbox) MarkRead(id string) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for i := range b.messages {
		if b.messages[i].ID == id {
			b.messages[i].Unread = false
		}
	}
}

func (b *Mailbox) Sync(ctx context.Context) error {
	select {
	case <-time.After(1500 * time.Millisecond):
		return nil

	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Mailbox) Send(draft Draft) error {
	time.Sleep(600 * time.Millisecond)

	b.mutex.Lock()
	defer b.mutex.Unlock()

	accountIndex := slices.IndexFunc(b.accounts, func(a Account) bool { return a.Name == draft.Account })
	if accountIndex < 0 {
		return fmt.Errorf("no account called %q", draft.Account)
	}

	var recipients []Address

	for _, email := range strings.Split(draft.To, ",") {
		email = strings.TrimSpace(email)
		if email != "" {
			recipients = append(recipients, Address{Email: email})
		}
	}

	b.nextID++

	b.messages = append(b.messages, Message{
		ID:      fmt.Sprintf("sent-%d", b.nextID),
		Account: draft.Account,
		Folder:  Sent,
		From:    Address{Email: b.accounts[accountIndex].Email},
		To:      recipients,
		Subject: draft.Subject,
		Date:    time.Now(),
		Text:    draft.Body,
	})

	return nil
}

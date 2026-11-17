package imapsync

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

type Folder struct {
	Name       string
	SpecialUse string
}

type Address struct {
	Name  string
	Email string
}

type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

type Fetched struct {
	UID         uint32
	Seen        bool
	From        Address
	To          []Address
	Cc          []Address
	Bcc         []Address
	Subject     string
	Date        time.Time
	MessageID   string
	References  []string
	Text        string
	HTML        string
	Attachments []Attachment
}

type Session struct {
	client  *imapclient.Client
	conn    *watchedConn
	changes chan struct{}
	stop    func() bool
}

const stallTimeout = time.Minute

// go-imap sets no read deadline while it waits for a response to start, so a command sent into a dead network would wait forever. It also retries reads after a timeout, so the connection closes on the first one.
type watchedConn struct {
	net.Conn
	mutex    sync.Mutex
	watching bool
}

func (c *watchedConn) Read(buffer []byte) (int, error) {
	c.mutex.Lock()

	if c.watching {
		c.Conn.SetReadDeadline(time.Now().Add(stallTimeout))
	}

	c.mutex.Unlock()

	count, err := c.Conn.Read(buffer)

	if errors.Is(err, os.ErrDeadlineExceeded) {
		c.Conn.Close()
	}

	return count, err
}

func (c *watchedConn) watch(watching bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.watching = watching

	deadline := time.Time{}
	if watching {
		deadline = time.Now().Add(stallTimeout)
	}

	c.Conn.SetReadDeadline(deadline)
}

func Dial(ctx context.Context, address, email, password string) (*Session, error) {
	host, port, err := net.SplitHostPort(address)

	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{
		Timeout:         20 * time.Second,
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 30 * time.Second, Interval: 10 * time.Second, Count: 3},
	}

	raw, err := dialer.DialContext(ctx, "tcp", address)

	if err != nil {
		return nil, err
	}

	conn := &watchedConn{Conn: raw}
	changes := make(chan struct{}, 1)

	notify := func() {
		select {
		case changes <- struct{}{}:

		default:
		}
	}

	options := &imapclient.Options{
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
		TLSConfig:   &tls.Config{ServerName: host},
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(*imapclient.UnilateralDataMailbox) { notify() },
			Expunge: func(uint32) { notify() },
			Fetch: func(message *imapclient.FetchMessageData) {
				message.Collect()
				notify()
			},
		},
	}

	conn.watch(true)

	var client *imapclient.Client

	if port == "993" {
		client = imapclient.New(tls.Client(conn, options.TLSConfig), options)
	} else {
		client, err = imapclient.NewStartTLS(conn, options)
	}

	if err != nil {
		raw.Close()
		return nil, err
	}

	stop := context.AfterFunc(ctx, func() { client.Close() })

	err = client.Login(email, password).Wait()

	if err != nil {
		stop()
		client.Close()
		return nil, err
	}

	conn.watch(false)

	return &Session{client: client, conn: conn, changes: changes, stop: stop}, nil
}

func (s *Session) Close() {
	s.stop()
	s.conn.watch(true)
	s.client.Logout().Wait()
	s.client.Close()
}

func (s *Session) Folders() ([]Folder, error) {
	s.conn.watch(true)
	defer s.conn.watch(false)

	listed, err := s.client.List("", "*", nil).Collect()

	if err != nil {
		return nil, err
	}

	specialUses := []imap.MailboxAttr{imap.MailboxAttrAll, imap.MailboxAttrArchive, imap.MailboxAttrDrafts, imap.MailboxAttrFlagged, imap.MailboxAttrJunk, imap.MailboxAttrSent, imap.MailboxAttrTrash}

	var folders []Folder

	for _, entry := range listed {
		folder := Folder{Name: entry.Mailbox}

		for _, attribute := range entry.Attrs {
			if slices.Contains(specialUses, attribute) {
				folder.SpecialUse = string(attribute)
			}
		}

		folders = append(folders, folder)
	}

	return folders, nil
}

func (s *Session) Select(name string) (uidValidity uint32, err error) {
	s.conn.watch(true)
	defer s.conn.watch(false)

	selected, err := s.client.Select(name, nil).Wait()

	if err != nil {
		return 0, err
	}

	return selected.UIDValidity, nil
}

func (s *Session) Search(since time.Time) ([]uint32, error) {
	s.conn.watch(true)
	defer s.conn.watch(false)

	found, err := s.client.UIDSearch(&imap.SearchCriteria{Since: since}, nil).Wait()

	if err != nil {
		return nil, err
	}

	var uids []uint32
	for _, uid := range found.AllUIDs() {
		uids = append(uids, uint32(uid))
	}

	return uids, nil
}

func (s *Session) MarkSeen(uids []uint32) error {
	s.conn.watch(true)
	defer s.conn.watch(false)

	for batch := range slices.Chunk(uids, 500) {
		err := s.client.Store(uidSet(batch), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close()

		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Session) Seen(uids []uint32) (map[uint32]bool, error) {
	s.conn.watch(true)
	defer s.conn.watch(false)

	seen := map[uint32]bool{}

	for batch := range slices.Chunk(uids, 500) {
		messages, err := s.client.Fetch(uidSet(batch), &imap.FetchOptions{UID: true, Flags: true}).Collect()

		if err != nil {
			return nil, err
		}

		for _, item := range messages {
			seen[uint32(item.UID)] = slices.Contains(item.Flags, imap.FlagSeen)
		}
	}

	return seen, nil
}

func (s *Session) Move(uids []uint32, destination string) (map[uint32]uint32, error) {
	if !s.client.Caps().Has(imap.CapUIDPlus) {
		return nil, errors.New("the server doesn't support UIDPLUS, which moving needs")
	}

	s.conn.watch(true)
	defer s.conn.watch(false)

	moved := map[uint32]uint32{}

	for batch := range slices.Chunk(uids, 500) {
		data, err := s.client.Move(uidSet(batch), destination).Wait()

		if err != nil {
			return nil, err
		}

		sourceSet, sourceOK := data.SourceUIDs.(imap.UIDSet)
		destinationSet, destinationOK := data.DestUIDs.(imap.UIDSet)

		if !sourceOK || !destinationOK {
			continue
		}

		sources, _ := sourceSet.Nums()
		destinations, _ := destinationSet.Nums()

		if len(sources) != len(destinations) {
			continue
		}

		for i, source := range sources {
			moved[uint32(source)] = uint32(destinations[i])
		}
	}

	return moved, nil
}

func (s *Session) AppendSeen(folder string, content []byte) error {
	s.conn.watch(true)
	defer s.conn.watch(false)

	command := s.client.Append(folder, int64(len(content)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}})

	_, err := command.Write(content)

	if err != nil {
		return err
	}

	err = command.Close()

	if err != nil {
		return err
	}

	_, err = command.Wait()

	return err
}

func (s *Session) HasMessageID(messageID string) (bool, error) {
	s.conn.watch(true)
	defer s.conn.watch(false)

	found, err := s.client.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}}}, nil).Wait()

	if err != nil {
		return false, err
	}

	return len(found.AllUIDs()) > 0, nil
}

func (s *Session) Idle(ctx context.Context, wait time.Duration) (changed bool, err error) {
	if !s.client.Caps().Has(imap.CapIdle) {
		return false, errors.New("the server doesn't support IDLE")
	}

	s.conn.watch(true)
	idle, err := s.client.Idle()
	s.conn.watch(false)

	if err != nil {
		return false, err
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-s.changes:
		changed = true

	case <-timer.C:

	case <-ctx.Done():

	case <-s.client.Closed():
	}

	s.conn.watch(true)
	defer s.conn.watch(false)

	err = idle.Close()

	if err == nil {
		err = idle.Wait()
	}

	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	return changed, err
}

func (s *Session) Fetch(uids []uint32, deliver func([]Fetched) error) error {
	sizes := map[uint32]int64{}

	for batch := range slices.Chunk(uids, 500) {
		s.conn.watch(true)
		messages, err := s.client.Fetch(uidSet(batch), &imap.FetchOptions{UID: true, RFC822Size: true}).Collect()
		s.conn.watch(false)

		if err != nil {
			return err
		}

		for _, item := range messages {
			sizes[uint32(item.UID)] = item.RFC822Size
		}
	}

	var batches [][]uint32
	var current []uint32
	var currentSize int64

	for _, uid := range uids {
		if len(current) > 0 && (len(current) == 50 || currentSize+sizes[uid] > 32<<20) {
			batches = append(batches, current)
			current = nil
			currentSize = 0
		}

		current = append(current, uid)
		currentSize += sizes[uid]
	}

	if len(current) > 0 {
		batches = append(batches, current)
	}

	section := &imap.FetchItemBodySection{Peek: true}

	for _, batch := range batches {
		s.conn.watch(true)
		messages, err := s.client.Fetch(uidSet(batch), &imap.FetchOptions{UID: true, Flags: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
		s.conn.watch(false)

		if err != nil {
			return err
		}

		var fetchedBatch []Fetched

		for _, item := range messages {
			fetched := Fetched{UID: uint32(item.UID), Seen: slices.Contains(item.Flags, imap.FlagSeen)}

			reader, err := mail.CreateReader(bytes.NewReader(item.FindBodySection(section)))

			if err != nil && !message.IsUnknownCharset(err) {
				fetched.Text = "scry couldn't read this email: " + err.Error()
				fetchedBatch = append(fetchedBatch, fetched)
				continue
			}

			fetched.Subject, _ = reader.Header.Subject()
			fetched.Date, _ = reader.Header.Date()
			fetched.MessageID, _ = reader.Header.MessageID()
			fetched.References, _ = reader.Header.MsgIDList("References")

			from, _ := reader.Header.AddressList("From")

			if len(from) > 0 {
				fetched.From = Address{Name: from[0].Name, Email: from[0].Address}
			}

			for _, recipients := range []struct {
				key  string
				list *[]Address
			}{{"To", &fetched.To}, {"Cc", &fetched.Cc}, {"Bcc", &fetched.Bcc}} {
				addresses, _ := reader.Header.AddressList(recipients.key)

				for _, address := range addresses {
					*recipients.list = append(*recipients.list, Address{Name: address.Name, Email: address.Address})
				}
			}

			inlineImages := map[string]string{}

			for {
				part, err := reader.NextPart()

				if errors.Is(err, io.EOF) {
					break
				}

				if part == nil || (err != nil && !message.IsUnknownCharset(err)) {
					break
				}

				body, err := io.ReadAll(part.Body)

				if err != nil {
					continue
				}

				contentType, parameters, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
				contentID := strings.Trim(part.Header.Get("Content-Id"), "<> ")
				_, isAttachment := part.Header.(*mail.AttachmentHeader)

				switch {
				case contentID != "" && strings.HasPrefix(contentType, "image/"):
					inlineImages[contentID] = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(body)

				case !isAttachment && contentType == "text/plain" && fetched.Text == "":
					fetched.Text = string(body)

				case !isAttachment && contentType == "text/html" && fetched.HTML == "":
					fetched.HTML = string(body)

				case isAttachment || parameters["name"] != "" || strings.HasPrefix(contentType, "image/"):
					name := parameters["name"]

					attachmentHeader, ok := part.Header.(*mail.AttachmentHeader)

					if ok {
						filename, err := attachmentHeader.Filename()

						if err == nil && filename != "" {
							name = filename
						}
					}

					if name == "" {
						name = "attachment"
					}

					fetched.Attachments = append(fetched.Attachments, Attachment{Name: name, ContentType: contentType, Data: body})
				}
			}

			for contentID, dataURI := range inlineImages {
				fetched.HTML = strings.ReplaceAll(fetched.HTML, "cid:"+contentID, dataURI)
			}

			if fetched.Text == "" && fetched.HTML != "" {
				fetched.Text = PlainText(fetched.HTML)
			}

			if fetched.Date.IsZero() {
				fetched.Date = item.InternalDate
			}

			fetchedBatch = append(fetchedBatch, fetched)
		}

		err = deliver(fetchedBatch)

		if err != nil {
			return err
		}
	}

	return nil
}

func PlainText(document string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(document))
	var text strings.Builder
	skipping := ""

	for {
		tokenType := tokenizer.Next()

		if tokenType == html.ErrorToken {
			break
		}

		token := tokenizer.Token()

		switch {
		case tokenType == html.StartTagToken && (token.Data == "script" || token.Data == "style" || token.Data == "head"):
			skipping = token.Data

		case tokenType == html.EndTagToken && token.Data == skipping:
			skipping = ""

		case tokenType == html.TextToken && skipping == "":
			text.WriteString(token.Data)

		case tokenType == html.StartTagToken && (token.Data == "br" || token.Data == "p" || token.Data == "div" || token.Data == "tr" || token.Data == "li"):
			text.WriteString("\n")
		}
	}

	return strings.TrimSpace(text.String())
}

func uidSet(uids []uint32) imap.UIDSet {
	var set imap.UIDSet

	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}

	return set
}

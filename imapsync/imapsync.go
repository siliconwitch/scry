package imapsync

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"slices"
	"strings"
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
	Subject     string
	Date        time.Time
	Text        string
	HTML        string
	Attachments []Attachment
}

type Session struct {
	client *imapclient.Client
	stop   func() bool
}

func Dial(ctx context.Context, address, email, password string) (*Session, error) {
	options := &imapclient.Options{WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}}

	_, port, _ := net.SplitHostPort(address)

	var client *imapclient.Client
	var err error

	if port == "993" {
		client, err = imapclient.DialTLS(address, options)
	} else {
		client, err = imapclient.DialStartTLS(address, options)
	}

	if err != nil {
		return nil, err
	}

	stop := context.AfterFunc(ctx, func() { client.Close() })

	err = client.Login(email, password).Wait()

	if err != nil {
		stop()
		client.Close()
		return nil, err
	}

	return &Session{client: client, stop: stop}, nil
}

func (s *Session) Close() {
	s.stop()
	s.client.Logout().Wait()
	s.client.Close()
}

func (s *Session) Folders() ([]Folder, error) {
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
	selected, err := s.client.Select(name, nil).Wait()

	if err != nil {
		return 0, err
	}

	return selected.UIDValidity, nil
}

func (s *Session) Search(since time.Time) ([]uint32, error) {
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
	for batch := range slices.Chunk(uids, 500) {
		err := s.client.Store(uidSet(batch), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close()

		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Session) Seen(uids []uint32) (map[uint32]bool, error) {
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

func (s *Session) Fetch(uids []uint32, deliver func([]Fetched) error) error {
	sizes := map[uint32]int64{}

	for batch := range slices.Chunk(uids, 500) {
		messages, err := s.client.Fetch(uidSet(batch), &imap.FetchOptions{UID: true, RFC822Size: true}).Collect()

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
		messages, err := s.client.Fetch(uidSet(batch), &imap.FetchOptions{UID: true, Flags: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()

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

			from, _ := reader.Header.AddressList("From")

			if len(from) > 0 {
				fetched.From = Address{Name: from[0].Name, Email: from[0].Address}
			}

			to, _ := reader.Header.AddressList("To")

			for _, address := range to {
				fetched.To = append(fetched.To, Address{Name: address.Name, Email: address.Address})
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
				tokenizer := html.NewTokenizer(strings.NewReader(fetched.HTML))
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

				fetched.Text = strings.TrimSpace(text.String())
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

func uidSet(uids []uint32) imap.UIDSet {
	var set imap.UIDSet

	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}

	return set
}

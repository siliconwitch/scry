package viewer

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

//go:embed page.html app.js
var assets embed.FS

type Attachment struct {
	Name string
	Data []byte
}

type Address struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type Page struct {
	ID           string
	Subject      string
	From         Address
	To           []Address
	Cc           []Address
	Bcc          []Address
	Date         string
	HTML         string
	Text         string
	Attachments  []Attachment
	AccountLight string
	AccountDark  string
}

type Server struct {
	mutex     sync.Mutex
	page      Page
	version   int
	listeners map[chan struct{}]bool
	done      chan struct{}
	listener  net.Listener
	token     string
}

func Start() (*Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		return nil, err
	}

	tokenBytes := make([]byte, 24)
	rand.Read(tokenBytes)

	server := &Server{
		listeners: map[chan struct{}]bool{},
		done:      make(chan struct{}),
		listener:  listener,
		token:     hex.EncodeToString(tokenBytes),
	}

	go http.Serve(listener, server)

	return server, nil
}

func (s *Server) URL() string {
	return "http://" + s.listener.Addr().String() + "/" + s.token + "/"
}

func (s *Server) Show(page Page) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.page = page
	s.version++

	for listener := range s.listeners {
		select {
		case listener <- struct{}{}:
		default:
		}
	}
}

func (s *Server) Close() {
	close(s.done)
	s.listener.Close()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.listener.Addr().String() || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}

	path, ok := strings.CutPrefix(r.URL.Path, "/"+s.token+"/")

	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")

	parts := strings.Split(path, "/")

	switch {
	case path == "":
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; frame-src 'self'; connect-src 'self'; img-src data:")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		content, _ := assets.ReadFile("page.html")

		w.Write(content)

	case path == "app.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		content, _ := assets.ReadFile("app.js")

		w.Write(content)

	case path == "events":
		flusher, ok := w.(http.Flusher)

		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		changed := make(chan struct{}, 1)
		changed <- struct{}{}

		s.mutex.Lock()
		s.listeners[changed] = true
		s.mutex.Unlock()

		defer func() {
			s.mutex.Lock()
			delete(s.listeners, changed)
			s.mutex.Unlock()
		}()

		w.Header().Set("Content-Type", "text/event-stream")

		for {
			select {
			case <-changed:
				fmt.Fprint(w, "data: changed\n\n")
				flusher.Flush()

			case <-r.Context().Done():
				return

			case <-s.done:
				return
			}
		}

	case path == "current":
		s.mutex.Lock()
		page := s.page
		version := s.version
		s.mutex.Unlock()

		type attachmentInfo struct {
			Name string `json:"name"`
			Size int    `json:"size"`
			URL  string `json:"url"`
		}

		attachments := []attachmentInfo{}
		for i, attachment := range page.Attachments {
			attachments = append(attachments, attachmentInfo{
				Name: attachment.Name,
				Size: len(attachment.Data),
				URL:  fmt.Sprintf("attachment/%d/%d", version, i),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"empty":        page.ID == "",
			"accountLight": page.AccountLight,
			"accountDark":  page.AccountDark,
			"subject":      page.Subject,
			"from":         page.From,
			"to":           page.To,
			"cc":           page.Cc,
			"bcc":          page.Bcc,
			"date":         page.Date,
			"attachments":  attachments,
			"body":         fmt.Sprintf("body/%d", version),
		})

	case len(parts) == 2 && parts[0] == "body":
		s.mutex.Lock()
		page := s.page
		current := strconv.Itoa(s.version) == parts[1]
		s.mutex.Unlock()

		if !current {
			http.Error(w, "This email is no longer selected.", http.StatusGone)
			return
		}

		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; font-src data:; img-src data:; sandbox allow-popups allow-popups-to-escape-sandbox")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if page.HTML != "" {
			document, err := html.Parse(strings.NewReader(page.HTML))

			if err != nil {
				http.Error(w, "This email can't be shown.", http.StatusInternalServerError)
				return
			}

			for node := range document.Descendants() {
				if node.DataAtom == atom.Head {
					node.InsertBefore(&html.Node{Type: html.ElementNode, Data: "base", DataAtom: atom.Base, Attr: []html.Attribute{{Key: "target", Val: "_blank"}}}, node.FirstChild)
					break
				}
			}

			html.Render(w, document)
			return
		}

		fmt.Fprint(w, `<!doctype html><base target="_blank"><body style="margin:0;padding:28px 32px;background:#fff;color:#1f1d24">`,
			`<div style="white-space:pre-wrap;font:16px/1.6 system-ui,sans-serif;max-width:72ch">`, html.EscapeString(page.Text), `</div></body>`)

	case len(parts) == 3 && parts[0] == "attachment":
		s.mutex.Lock()
		page := s.page
		current := strconv.Itoa(s.version) == parts[1]
		s.mutex.Unlock()

		index, err := strconv.Atoi(parts[2])

		if !current || err != nil || index < 0 || index >= len(page.Attachments) {
			http.NotFound(w, r)
			return
		}

		attachment := page.Attachments[index]

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Name}))
		w.Write(attachment.Data)

	default:
		http.NotFound(w, r)
	}
}

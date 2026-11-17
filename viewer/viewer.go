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
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/net/html"
)

//go:embed page.html app.js pdfjs
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

type event struct {
	name string
	data string
}

type Server struct {
	mutex        sync.Mutex
	page         Page
	version      int
	links        []string
	linksVersion int
	listeners    map[chan event]bool
	done         chan struct{}
	listener     net.Listener
	token        string
	linkCommand  string
}

func Start(linkCommand string) (*Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		return nil, err
	}

	tokenBytes := make([]byte, 24)
	rand.Read(tokenBytes)

	server := &Server{
		listeners:   map[chan event]bool{},
		done:        make(chan struct{}),
		listener:    listener,
		token:       hex.EncodeToString(tokenBytes),
		linkCommand: linkCommand,
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

	s.broadcast(event{name: "message", data: "changed"})
}

func (s *Server) broadcast(sent event) {
	for listener := range s.listeners {
		select {
		case listener <- sent:
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
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; worker-src 'self'; style-src 'unsafe-inline'; frame-src 'self'; connect-src 'self'; img-src 'self' data:")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		content, _ := assets.ReadFile("page.html")

		w.Write(content)

	case path == "app.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		content, _ := assets.ReadFile("app.js")

		w.Write(content)

	case parts[0] == "pdfjs":
		content, err := assets.ReadFile(path)

		if err != nil {
			http.NotFound(w, r)
			return
		}

		contentType := map[string]string{".mjs": "text/javascript; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".wasm": "application/wasm"}[filepath.Ext(path)]

		if contentType == "" {
			contentType = "application/octet-stream"
		}

		// The PDF worker takes its policy from this response, and decodes some images with WebAssembly.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self'")
		w.Header().Set("Content-Type", contentType)
		w.Write(content)

	case path == "events":
		flusher, ok := w.(http.Flusher)

		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		changed := make(chan event, 8)
		changed <- event{name: "message", data: "changed"}

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
			case sent := <-changed:
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sent.name, strings.ReplaceAll(sent.data, "\n", " "))
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
			Kind string `json:"kind"`
			View string `json:"view"`
		}

		attachments := []attachmentInfo{}
		for i, attachment := range page.Attachments {
			info := attachmentInfo{
				Name: attachment.Name,
				Size: len(attachment.Data),
				URL:  fmt.Sprintf("attachment/%d/%d", version, i),
				Kind: viewableKinds[http.DetectContentType(attachment.Data)],
			}

			if info.Kind != "" {
				info.View = fmt.Sprintf("view/%d/%d", version, i)
			}

			attachments = append(attachments, info)
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
		version := s.version
		current := strconv.Itoa(version) == parts[1]
		s.mutex.Unlock()

		if !current {
			http.Error(w, "This email is no longer selected.", http.StatusGone)
			return
		}

		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; font-src data:; img-src data:; sandbox")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if page.HTML == "" {
			fmt.Fprint(w, `<!doctype html><body style="margin:0;padding:28px 32px;background:#fff;color:#1f1d24">`,
				`<div style="white-space:pre-wrap;font:16px/1.6 system-ui,sans-serif;max-width:72ch">`, html.EscapeString(page.Text), `</div></body>`)
			return
		}

		// Chromium parses noscript content as markup in a frame without scripts, so the tree must too.
		document, err := html.ParseWithOptions(strings.NewReader(page.HTML), html.ParseOptionEnableScripting(false))

		if err != nil {
			http.Error(w, "This email can't be shown.", http.StatusInternalServerError)
			return
		}

		var links []string
		var removed []*html.Node

		for node := range document.Descendants() {
			if node.Type != html.ElementNode {
				continue
			}

			switch node.Data {
			// Chromium connects to the address in some of these despite the CSP and the sandbox.
			case "link", "meta", "base", "iframe", "frame", "frameset", "object", "embed", "portal", "fencedframe":
				removed = append(removed, node)
				continue

			case "a", "area":

			default:
				continue
			}

			href := ""

			node.Attr = slices.DeleteFunc(node.Attr, func(attribute html.Attribute) bool {
				isHref := attribute.Key == "href" && (attribute.Namespace == "" || attribute.Namespace == "xlink")

				if isHref {
					href = attribute.Val
				}

				return isHref || attribute.Key == "target" || attribute.Key == "ping"
			})

			address, err := url.Parse(strings.TrimSpace(href))

			if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
				continue
			}

			node.Attr = append(node.Attr, html.Attribute{Key: "href", Val: fmt.Sprintf("/%s/link/%d/%d", s.token, version, len(links))})
			links = append(links, address.String())
		}

		for _, node := range removed {
			node.Parent.RemoveChild(node)
		}

		s.mutex.Lock()
		s.links = links
		s.linksVersion = version
		s.mutex.Unlock()

		html.Render(w, document)

	case len(parts) == 3 && parts[0] == "link":
		s.mutex.Lock()
		links := s.links
		current := parts[1] == strconv.Itoa(s.version) && parts[1] == strconv.Itoa(s.linksVersion)
		s.mutex.Unlock()

		index, err := strconv.Atoi(parts[2])

		// No content keeps the email in its frame.
		w.WriteHeader(http.StatusNoContent)

		if !current || err != nil || index < 0 || index >= len(links) {
			return
		}

		s.openLink(links[index])

	case path == "open":
		w.WriteHeader(http.StatusNoContent)

		s.openLink(r.URL.Query().Get("url"))

	case len(parts) == 3 && parts[0] == "view":
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
		contentType := http.DetectContentType(attachment.Data)

		if viewableKinds[contentType] == "" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": attachment.Name}))
		w.Write(attachment.Data)

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

// The kinds the viewer shows in place, keyed by the type sniffed from the attachment's bytes, never its claimed type.
var viewableKinds = map[string]string{
	"application/pdf": "pdf",
	"image/png":       "image",
	"image/jpeg":      "image",
	"image/gif":       "image",
	"image/webp":      "image",
	"image/bmp":       "image",
}

func (s *Server) notice(text string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.broadcast(event{name: "notice", data: text})
}

func (s *Server) openLink(link string) {
	address, err := url.Parse(strings.TrimSpace(link))

	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
		return
	}

	words := strings.Fields(s.linkCommand)

	if len(words) == 0 {
		s.notice(`Set links in the config, such as links = "firefox %s", to open links.`)
		return
	}

	for i := range words {
		words[i] = strings.ReplaceAll(words[i], "%s", address.String())
	}

	browser := exec.Command(words[0], words[1:]...)

	// A browser that scry starts must outlive the terminal scry runs in.
	browser.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	err = browser.Start()

	if err != nil {
		s.notice("Can't open the link: " + err.Error())
		return
	}

	go browser.Wait()
}

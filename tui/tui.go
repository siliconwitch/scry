package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/siliconwitch/scry/mailbox"
	"github.com/siliconwitch/scry/synclog"
	"github.com/siliconwitch/scry/viewer"
)

type Options struct {
	Mail         *mailbox.Mailbox
	Viewer       *viewer.Server
	Log          *synclog.Log
	LogDirectory string
	StartNotice  string
}

type pane int

const (
	folderPane pane = iota
	listPane
)

type popup int

const (
	noPopup popup = iota
	movePopup
	discardPopup
	logPopup
)

type folderRow struct {
	account string
	folder  string
}

type syncDoneMsg struct{ err error }

type sendDoneMsg struct{ err error }

type sendTickMsg struct{ generation int }

type noticeExpiredMsg struct{ generation int }

type editorDoneMsg struct {
	path string
	err  error
}

const sendDelay = 5

const folderPaneWidth = 24

type adaptiveColor struct {
	Light string
	Dark  string
}

var (
	accentColor = adaptiveColor{Light: "#7C3AED", Dark: "#C4A2F0"}
	mutedColor  = adaptiveColor{Light: "#6B7280", Dark: "#8C8C8C"}
	yellowColor = adaptiveColor{Light: "#B45309", Dark: "#D9B44A"}
	redColor    = adaptiveColor{Light: "#B91C1C", Dark: "#E06C6C"}

	accountColors = []adaptiveColor{
		{Light: "#1D4ED8", Dark: "#60A5FA"},
		{Light: "#C2410C", Dark: "#FB923C"},
		{Light: "#15803D", Dark: "#79C879"},
		{Light: "#0E7490", Dark: "#67D4E8"},
		{Light: "#BE185D", Dark: "#F472B6"},
	}

	boldStyle = lipgloss.NewStyle().Bold(true)
)

var clickableKeys = []string{"A", "M", "!", "D", "r", "n", "ctrl+r", "L", "q", "/", "e", "s", "h", "x", "u", "w", "y", "enter", "esc"}

type Model struct {
	options Options
	zones   *zone.Manager

	width           int
	height          int
	lightBackground bool

	accounts     []mailbox.Account
	folderRows   []folderRow
	folderIndex  int
	messages     []mailbox.Message
	messageIndex int
	listOffset   int
	focus        pane

	search        textinput.Model
	searchFocused bool

	shownID     string
	shownImages bool

	networkActive int
	networkError  string

	draft         *mailbox.Draft
	draftVisible  bool
	draftError    string
	sending       bool
	delivering    bool
	sendRemaining int
	sendGen       int

	popup          popup
	moveTargets    []string
	moveIndex      int
	logLines       []string
	logOffset      int
	pendingCompose bool
	pendingReplyID string

	notice           string
	noticeGeneration int
}

func New(options Options) *Model {
	search := textinput.New()
	search.Prompt = ""
	search.Placeholder = "Search all mail"

	m := &Model{
		options:  options,
		zones:    zone.New(),
		accounts: options.Mail.Accounts(),
		search:   search,
		focus:    listPane,
		notice:   options.StartNotice,
	}

	m.folderRows = append(m.folderRows, folderRow{folder: mailbox.Inbox})

	for _, account := range m.accounts {
		for _, folder := range account.Folders {
			m.folderRows = append(m.folderRows, folderRow{account: account.Name, folder: folder})
		}
	}

	m.reload()
	m.showSelected()

	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.startSync())
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.lightBackground = !msg.IsDark()
		m.search.SetStyles(textinput.DefaultStyles(msg.IsDark()))
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.search.SetWidth(min(48, m.width/2) - 3)
		m.clampList()
		return m, nil

	case tea.KeyPressMsg:
		if m.searchFocused && m.popup == noPopup {
			return m.handleSearchKey(msg)
		}

		return m.handleKey(msg.String())

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case syncDoneMsg:
		if msg.err != nil {
			m.options.Log.Write("Sync failed: %v", msg.err)
		} else {
			m.options.Log.Write("Sync finished")
		}

		m.reload()
		m.showSelected()
		m.finishNetwork(msg.err)

		return m, nil

	case sendTickMsg:
		if msg.generation != m.sendGen || !m.sending {
			return m, nil
		}

		m.sendRemaining--

		if m.sendRemaining > 0 {
			return m, m.sendTick()
		}

		m.sending = false
		m.delivering = true
		m.networkActive++

		draft := *m.draft
		mail := m.options.Mail

		return m, func() tea.Msg { return sendDoneMsg{err: mail.Send(draft)} }

	case sendDoneMsg:
		m.delivering = false

		if msg.err != nil {
			m.options.Log.Write("Sending from %s failed: %v", m.draft.Account, msg.err)
			m.draftError = "Couldn't send: " + msg.err.Error()
			m.draftVisible = true
			m.finishNetwork(msg.err)
			return m, nil
		}

		m.options.Log.Write("Sent a message from %s", m.draft.Account)
		account := m.draft.Account
		m.draft = nil
		m.draftVisible = false
		m.reload()

		m.finishNetwork(nil)

		return m, m.setNotice("Sent from " + account)

	case noticeExpiredMsg:
		if msg.generation == m.noticeGeneration {
			m.notice = ""
		}

		return m, nil

	case editorDoneMsg:
		content, readErr := os.ReadFile(msg.path)
		os.Remove(msg.path)

		if msg.err != nil || readErr != nil {
			return m, m.setNotice("The editor failed: " + errors.Join(msg.err, readErr).Error())
		}

		m.draftError = ""

		headerBlock, body, _ := strings.Cut(string(content), "\n\n")

		edited := mailbox.Draft{ReplyTo: m.draft.ReplyTo, Body: strings.TrimRight(body, "\n")}

		for _, line := range strings.Split(headerBlock, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}

			key, value, _ := strings.Cut(line, ":")
			value = strings.TrimSpace(value)

			switch strings.ToLower(strings.TrimSpace(key)) {
			case "from":
				edited.Account = value

			case "to":
				edited.To = value

			case "subject":
				edited.Subject = value

			case "attach":
				if value != "" {
					edited.Attach = append(edited.Attach, value)
				}

			default:
				m.draftError = fmt.Sprintf("Unknown header line %q. Use From, To, Subject or Attach.", line)
			}
		}

		if edited.ReplyTo != "" && edited.Account != m.draft.Account {
			m.draftError = "Replies always go from " + m.draft.Account + "."
			edited.Account = m.draft.Account
		}

		if _, ok := m.options.Mail.Account(edited.Account); !ok && m.draftError == "" {
			var names []string
			for _, account := range m.accounts {
				names = append(names, account.Name)
			}

			m.draftError = fmt.Sprintf("No account called %q. Use one of: %s.", edited.Account, strings.Join(names, ", "))
		}

		m.draft = &edited
		m.draftVisible = true

		return m, nil
	}

	return m, nil
}

func (m *Model) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc":
		m.search.SetValue("")
		m.search.Blur()
		m.searchFocused = false
		m.messageIndex = 0
		m.listOffset = 0
		m.reload()
		m.showSelected()
		return m, nil

	case "enter", "tab":
		m.search.Blur()
		m.searchFocused = false
		m.focus = listPane
		return m, nil

	case "up", "ctrl+p":
		m.focus = listPane
		return m.handleKey("k")

	case "down", "ctrl+n":
		m.focus = listPane
		return m.handleKey("j")
	}

	previous := m.search.Value()

	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)

	if m.search.Value() != previous {
		m.messageIndex = 0
		m.listOffset = 0
		m.reload()
		m.showSelected()
	}

	return m, cmd
}

func (m *Model) handleKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.popup {
	case movePopup:
		switch key {
		case "j", "down":
			m.moveIndex = min(m.moveIndex+1, len(m.moveTargets)-1)

		case "k", "up":
			m.moveIndex = max(m.moveIndex-1, 0)

		case "enter":
			m.popup = noPopup
			return m, m.moveSelected(m.moveTargets[m.moveIndex])

		case "esc", "q":
			m.popup = noPopup
		}

		return m, nil

	case discardPopup:
		switch key {
		case "y":
			m.popup = noPopup
			m.draft = nil
			m.draftVisible = false
			m.draftError = ""

			if m.pendingCompose {
				return m, m.startCompose(m.pendingReplyID)
			}

			return m, m.setNotice("Draft discarded")

		case "n", "esc", "q":
			m.popup = noPopup
		}

		return m, nil

	case logPopup:
		visibleLines := max(1, m.panesHeight()-4)

		switch key {
		case "j", "down":
			m.logOffset = min(m.logOffset+1, max(0, len(m.logLines)-visibleLines))

		case "k", "up":
			m.logOffset = max(m.logOffset-1, 0)

		case "esc", "q", "L":
			m.popup = noPopup
		}

		return m, nil
	}

	if m.delivering {
		return m, nil
	}

	if m.sending {
		if key == "u" || key == "esc" {
			m.sending = false
			m.sendGen++
			m.draftVisible = true
			return m, m.setNotice("Send stopped. The email is back in the draft.")
		}

		return m, nil
	}

	switch key {
	case "q":
		return m, tea.Quit

	case "ctrl+r":
		if m.networkActive > 0 {
			return m, nil
		}

		return m, m.startSync()

	case "L":
		m.logLines = m.options.Log.Lines()
		m.logOffset = max(0, len(m.logLines)-max(1, m.panesHeight()-4))
		m.popup = logPopup
		return m, nil

	case "w":
		if m.draft != nil {
			m.draftVisible = true
		}

		return m, nil

	case "n":
		return m, m.requestCompose("")

	case "/":
		m.draftVisible = false
		m.searchFocused = true
		return m, m.search.Focus()
	}

	if m.draftVisible {
		switch key {
		case "e":
			return m, m.openEditor()

		case "s":
			if m.draftError != "" {
				return m, nil
			}

			if strings.TrimSpace(m.draft.To) == "" {
				m.draftError = "Add a recipient on the To line before sending."
				return m, nil
			}

			for _, path := range m.draft.Attach {
				if _, err := os.Stat(expandHome(path)); err != nil {
					m.draftError = "Can't find the attachment " + path + "."
					return m, nil
				}
			}

			m.sending = true
			m.sendRemaining = sendDelay
			m.sendGen++

			return m, m.sendTick()

		case "h", "esc":
			m.draftVisible = false

		case "x":
			m.pendingCompose = false
			m.popup = discardPopup
		}

		return m, nil
	}

	selected, hasSelection := m.selected()

	switch key {
	case "tab", "h", "l", "left", "right":
		if m.focus == folderPane {
			m.focus = listPane
		} else {
			m.focus = folderPane
		}

	case "enter":
		if m.focus == folderPane {
			m.focus = listPane
		}

	case "j", "down", "k", "up", "pgdown", "pgup", "home", "end":
		step := map[string]int{"j": 1, "down": 1, "k": -1, "up": -1, "pgdown": m.listRows(), "pgup": -m.listRows(), "home": -1 << 30, "end": 1 << 30}[key]

		if m.focus == folderPane {
			m.selectFolder(m.folderIndex + step)
			return m, nil
		}

		m.messageIndex = max(0, min(m.messageIndex+step, len(m.messages)-1))
		m.clampList()
		m.showSelected()

	case "esc":
		if m.search.Value() != "" {
			m.search.SetValue("")
			m.messageIndex = 0
			m.reload()
			m.showSelected()
		}

	case "A":
		return m, m.moveSelected(mailbox.Archive)

	case "!":
		return m, m.moveSelected(mailbox.Spam)

	case "D":
		return m, m.moveSelected(mailbox.Trash)

	case "M":
		if !hasSelection {
			return m, nil
		}

		account, _ := m.options.Mail.Account(selected.Account)

		m.moveTargets = nil
		for _, folder := range account.Folders {
			if folder != selected.Folder && folder != mailbox.Sent && folder != mailbox.Spam && folder != mailbox.Trash {
				m.moveTargets = append(m.moveTargets, folder)
			}
		}

		m.moveIndex = 0
		m.popup = movePopup

	case "r":
		if hasSelection {
			return m, m.requestCompose(selected.ID)
		}

	}

	return m, nil
}

func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	mouse := msg.Mouse()

	if _, wheel := msg.(tea.MouseWheelMsg); wheel {
		key := "j"
		if mouse.Button == tea.MouseWheelUp {
			key = "k"
		}

		switch {
		case m.popup != noPopup:
			return m.handleKey(key)

		case m.zones.Get("pane:folders").InBounds(msg):
			m.focus = folderPane

		case m.zones.Get("pane:list").InBounds(msg):
			m.focus = listPane

		default:
			return m, nil
		}

		return m.handleKey(key)
	}

	if _, click := msg.(tea.MouseClickMsg); !click || mouse.Button != tea.MouseLeft {
		return m, nil
	}

	if m.zones.Get("search").InBounds(msg) {
		return m.handleKey("/")
	}

	if m.searchFocused {
		m.search.Blur()
		m.searchFocused = false
	}

	for _, key := range clickableKeys {
		if m.zones.Get("key:" + key).InBounds(msg) {
			return m.handleKey(key)
		}
	}

	if m.popup == movePopup {
		for i := range m.moveTargets {
			if m.zones.Get(fmt.Sprintf("move:%d", i)).InBounds(msg) {
				m.moveIndex = i
				return m.handleKey("enter")
			}
		}

		return m, nil
	}

	if m.popup != noPopup {
		return m, nil
	}

	for i := range m.folderRows {
		if m.zones.Get(fmt.Sprintf("folder:%d", i)).InBounds(msg) {
			m.focus = folderPane
			m.draftVisible = false
			m.selectFolder(i)
			return m, nil
		}
	}

	for i := m.listOffset; i < min(len(m.messages), m.listOffset+m.listRows()); i++ {
		if m.zones.Get(fmt.Sprintf("message:%d", i)).InBounds(msg) {
			m.focus = listPane
			m.messageIndex = i
			m.showSelected()
			return m, nil
		}
	}

	return m, nil
}

func (m *Model) selectFolder(index int) {
	m.folderIndex = max(0, min(index, len(m.folderRows)-1))

	if m.search.Value() != "" {
		m.search.SetValue("")
	}

	m.messageIndex = 0
	m.listOffset = 0
	m.reload()
	m.showSelected()
}

func (m *Model) reload() {
	selectedID := ""
	if message, ok := m.selected(); ok {
		selectedID = message.ID
	}

	query := strings.TrimSpace(m.search.Value())

	if query != "" {
		m.messages = mailbox.Search(m.options.Mail.All(), query)
	} else {
		row := m.folderRows[m.folderIndex]
		m.messages = m.options.Mail.Folder(row.account, row.folder)
	}

	if index := slices.IndexFunc(m.messages, func(message mailbox.Message) bool { return message.ID == selectedID }); index >= 0 {
		m.messageIndex = index
	}

	m.messageIndex = max(0, min(m.messageIndex, len(m.messages)-1))
	m.clampList()
}

func (m *Model) selected() (mailbox.Message, bool) {
	if m.messageIndex < 0 || m.messageIndex >= len(m.messages) {
		return mailbox.Message{}, false
	}

	return m.messages[m.messageIndex], true
}

func (m *Model) showSelected() {
	message, ok := m.selected()

	if !ok {
		if m.shownID != "" {
			m.options.Viewer.Show(viewer.Page{})
			m.shownID = ""
		}

		return
	}

	if message.ID == m.shownID {
		return
	}

	var recipients []string
	for _, recipient := range message.To {
		recipients = append(recipients, recipient.String())
	}

	var attachments []viewer.Attachment
	for _, attachment := range message.Attachments {
		attachments = append(attachments, viewer.Attachment{Name: attachment.Name, Data: attachment.Data})
	}

	m.options.Viewer.Show(viewer.Page{
		ID:           message.ID,
		Account:      message.Account,
		Subject:      message.Subject,
		From:         message.From.String(),
		To:           strings.Join(recipients, ", "),
		Date:         message.Date.Format("Mon 2 Jan 2006, 15:04"),
		HTML:         message.HTML,
		Text:         message.Text,
		Attachments:  attachments,
		AccountLight: m.accountColor(message.Account).Light,
		AccountDark:  m.accountColor(message.Account).Dark,
	})

	m.shownID = message.ID

	if message.Unread {
		m.options.Mail.MarkRead(message.ID)
		m.reload()
	}
}

func (m *Model) moveSelected(folder string) tea.Cmd {
	message, ok := m.selected()
	if !ok {
		return nil
	}

	err := m.options.Mail.Move(message.ID, folder)

	if errors.Is(err, mailbox.ErrAlreadyThere) {
		return m.setNotice("Already in " + folder)
	}

	if err != nil {
		return m.setNotice(err.Error())
	}

	m.options.Log.Write("Moved a message to %s in %s", folder, message.Account)

	m.reload()
	m.showSelected()

	return m.setNotice("Moved to " + folder)
}

func (m *Model) requestCompose(replyID string) tea.Cmd {
	if m.draft != nil {
		m.pendingCompose = true
		m.pendingReplyID = replyID
		m.popup = discardPopup
		return nil
	}

	return m.startCompose(replyID)
}

func (m *Model) startCompose(replyID string) tea.Cmd {
	draft := mailbox.Draft{Account: m.accounts[0].Name}

	if row := m.folderRows[m.folderIndex]; row.account != "" {
		draft.Account = row.account
	}

	if original, ok := m.options.Mail.Get(replyID); ok {
		subject := original.Subject
		if !strings.HasPrefix(strings.ToLower(subject), "re:") {
			subject = "Re: " + subject
		}

		var quoted strings.Builder
		for _, line := range strings.Split(original.Text, "\n") {
			quoted.WriteString("> " + line + "\n")
		}

		draft = mailbox.Draft{
			Account: original.Account,
			To:      original.From.Email,
			Subject: subject,
			Body:    "\n\nOn " + original.Date.Format("Mon 2 Jan 2006 at 15:04") + ", " + original.From.Short() + " wrote:\n" + quoted.String(),
			ReplyTo: original.ID,
		}
	}

	m.draft = &draft
	m.draftError = ""

	return m.openEditor()
}

func (m *Model) openEditor() tea.Cmd {
	directory := os.Getenv("XDG_RUNTIME_DIR")
	if directory == "" {
		directory = os.TempDir()
	}

	file, err := os.CreateTemp(directory, "scry-draft-*.txt")

	if err != nil {
		return m.setNotice("Can't create the draft file: " + err.Error())
	}

	var content strings.Builder

	content.WriteString("From: " + m.draft.Account + "\n")
	content.WriteString("To: " + m.draft.To + "\n")
	content.WriteString("Subject: " + m.draft.Subject + "\n")

	for _, path := range m.draft.Attach {
		content.WriteString("Attach: " + path + "\n")
	}

	if len(m.draft.Attach) == 0 {
		content.WriteString("Attach: \n")
	}

	content.WriteString("\n" + m.draft.Body + "\n")

	_, err = file.WriteString(content.String())

	file.Close()

	if err != nil {
		os.Remove(file.Name())
		return m.setNotice("Can't write the draft file: " + err.Error())
	}

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}

	if editor == "" {
		editor = "vi"
	}

	words := strings.Fields(editor)
	path := file.Name()

	command := exec.Command(words[0], append(words[1:], path)...)

	return tea.ExecProcess(command, func(err error) tea.Msg { return editorDoneMsg{path: path, err: err} })
}

func (m *Model) startSync() tea.Cmd {
	m.networkActive++
	m.options.Log.Write("Sync started")

	mail := m.options.Mail

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		return syncDoneMsg{err: mail.Sync(ctx)}
	}
}

func (m *Model) finishNetwork(err error) {
	m.networkActive--
	m.networkError = ""

	if err != nil {
		m.networkError = err.Error()
	}
}

func (m *Model) sendTick() tea.Cmd {
	generation := m.sendGen

	return tea.Tick(time.Second, func(time.Time) tea.Msg { return sendTickMsg{generation: generation} })
}

func (m *Model) setNotice(text string) tea.Cmd {
	m.notice = text
	m.noticeGeneration++
	generation := m.noticeGeneration

	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return noticeExpiredMsg{generation: generation} })
}

func (m *Model) panesHeight() int {
	return max(3, m.height-2)
}

func (m *Model) listRows() int {
	return max(1, m.panesHeight()-4)
}

func (m *Model) clampList() {
	rows := m.listRows()

	if m.messageIndex < m.listOffset {
		m.listOffset = m.messageIndex
	}

	if m.messageIndex >= m.listOffset+rows {
		m.listOffset = m.messageIndex - rows + 1
	}

	m.listOffset = max(0, min(m.listOffset, len(m.messages)-rows))
}

func (m *Model) View() tea.View {
	view := tea.NewView("Make the window at least 60 by 16 to use scry.")
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion

	if m.width < 60 || m.height < 16 {
		return view
	}

	panesHeight := m.panesHeight()
	listWidth := m.width - folderPaneWidth

	middle := lipgloss.JoinHorizontal(lipgloss.Top, m.folderView(panesHeight), m.rightView(listWidth, panesHeight))

	switch m.popup {
	case movePopup:
		var lines []string
		lines = append(lines, boldStyle.Render("Move to"), "")

		for i, folder := range m.moveTargets {
			line := "  " + folder
			if i == m.moveIndex {
				line = m.style(accentColor).Render("▌ " + folder)
			}

			lines = append(lines, m.zones.Mark(fmt.Sprintf("move:%d", i), fit(line, 26)))
		}

		middle = overlay(middle, m.popupBox(lines, 30))

	case discardPopup:
		question := "Discard this draft?"
		if m.pendingCompose {
			question = "Starting a new email discards the current draft."
		}

		lines := []string{boldStyle.Render(question), "", m.button("y", "discard") + "    " + m.button("n", "keep")}

		middle = overlay(middle, m.popupBox(lines, max(lipgloss.Width(question)+4, 34)))

	case logPopup:
		boxWidth := m.width - 8
		visibleLines := max(1, panesHeight-4)

		lines := []string{
			boldStyle.Render("Sync log") + m.style(mutedColor).Render("  "+m.options.LogDirectory),
			m.style(mutedColor).Render("Viewer  ") + m.options.Viewer.URL(),
			"",
		}

		if len(m.logLines) == 0 {
			lines = append(lines, m.style(mutedColor).Render("Nothing logged yet."))
		}

		for _, line := range m.logLines[m.logOffset:min(len(m.logLines), m.logOffset+visibleLines-2)] {
			lines = append(lines, fit(line, boxWidth-4))
		}

		middle = overlay(middle, m.popupBox(lines, boxWidth))
	}

	view.SetContent(m.zones.Scan(lipgloss.JoinVertical(lipgloss.Left, m.topBar(), middle, m.footer())))

	return view
}

func (m *Model) topBar() string {
	statusColor := accentColor
	switch {
	case m.networkActive > 0:
		statusColor = yellowColor

	case m.networkError != "":
		statusColor = redColor
	}

	title := " " + m.zones.Mark("key:L", m.style(statusColor).Render("◉")) + m.style(accentColor).Bold(true).Render(" scry ")

	searchWidth := min(48, m.width/2)

	searchText := m.search.View()
	if !m.searchFocused && m.search.Value() == "" {
		searchText = m.style(mutedColor).Render("Search all mail")
	}

	searchBorder := mutedColor
	if m.searchFocused {
		searchBorder = accentColor
	}

	searchBox := m.zones.Mark("search", m.style(searchBorder).Render("/ ")+fit(searchText, searchWidth-2))

	right := ""
	if m.draft != nil && !m.draftVisible {
		right = m.button("w", "draft") + " "
	}

	notice := m.notice
	if m.sending {
		notice = fmt.Sprintf("Sending in %ds. Press u to undo.", m.sendRemaining)
	}

	if m.delivering {
		notice = "Sending..."
	}

	noticeWidth := m.width - lipgloss.Width(title) - searchWidth - lipgloss.Width(right) - 4
	noticeText := lipgloss.PlaceHorizontal(max(0, noticeWidth), lipgloss.Right, m.style(mutedColor).Render(ansi.Truncate(notice, max(0, noticeWidth), "…")))

	return title + " " + searchBox + "  " + noticeText + " " + right
}

func (m *Model) folderView(height int) string {
	innerWidth := folderPaneWidth - 2
	innerHeight := height - 2

	var lines []string
	selectedLine := 0
	previousAccount := "-"

	for i, row := range m.folderRows {
		if row.account != previousAccount && row.account != "" {
			lines = append(lines, "", " "+m.style(m.accountColor(row.account)).Bold(true).Render(row.account))
		}

		previousAccount = row.account

		label := row.folder
		if row.account == "" {
			label = "All inboxes"
		}

		count := ""
		if unread := m.options.Mail.Unread(row.account, row.folder); unread > 0 {
			count = strconv.Itoa(unread)
		}

		indent := "  "
		if row.account == "" {
			indent = " "
		}

		text := indent + label
		gap := max(1, innerWidth-2-lipgloss.Width(text)-len(count))

		marker := "  "
		style := lipgloss.NewStyle()

		if i == m.folderIndex && m.search.Value() == "" {
			selectedLine = len(lines)
			markerColor := mutedColor
			if m.focus == folderPane && !m.draftVisible {
				markerColor = m.accountColor(row.account)
			}

			marker = m.style(markerColor).Render("▌") + " "
			style = style.Foreground(m.paint(markerColor)).Bold(true)
		}

		line := marker + style.Render(text) + strings.Repeat(" ", gap) + m.style(m.accountColor(row.account)).Render(count)

		lines = append(lines, m.zones.Mark(fmt.Sprintf("folder:%d", i), fit(line, innerWidth)))
	}

	offset := max(0, min(selectedLine-innerHeight/2, len(lines)-innerHeight))
	visible := lines[offset:min(len(lines), offset+innerHeight)]

	borderColor := mutedColor
	if m.focus == folderPane && !m.draftVisible && !m.searchFocused {
		borderColor = accentColor
	}

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.paint(borderColor)).Width(innerWidth + 2).Height(innerHeight + 2)

	return m.zones.Mark("pane:folders", box.Render(strings.Join(visible, "\n")))
}

func (m *Model) rightView(width, height int) string {
	innerWidth := width - 2
	innerHeight := height - 2

	if m.draftVisible && m.draft != nil {
		return m.draftView(innerWidth, innerHeight)
	}

	row := m.folderRows[m.folderIndex]

	title := row.folder
	switch {
	case m.search.Value() != "":
		title = "Search results"

	case row.account == "":
		title = "All inboxes"

	default:
		title = row.account + " · " + row.folder
	}

	lines := []string{" " + boldStyle.Render(title) + m.style(mutedColor).Render(fmt.Sprintf("  %d", len(m.messages))), ""}

	showAccount := m.search.Value() != "" || row.account == ""
	showFolder := m.search.Value() != ""

	accountWidth := 0
	for _, account := range m.accounts {
		accountWidth = max(accountWidth, lipgloss.Width(account.Name))
	}

	folderWidth := 9

	fromWidth := max(12, min(24, innerWidth/4))
	dateWidth := 6

	subjectWidth := innerWidth - 3 - fromWidth - 2 - 2 - dateWidth - 1
	if showAccount {
		subjectWidth -= accountWidth + 2
	}

	if showFolder {
		subjectWidth -= folderWidth + 2
	}

	if len(m.messages) == 0 {
		empty := "Nothing here"
		if m.search.Value() != "" {
			empty = "No matches"
		}

		lines = append(lines, "", lipgloss.PlaceHorizontal(innerWidth, lipgloss.Center, m.style(mutedColor).Render(empty)))
	}

	focused := m.focus == listPane && !m.searchFocused
	now := time.Now()

	for i := m.listOffset; i < min(len(m.messages), m.listOffset+m.listRows()); i++ {
		message := m.messages[i]

		marker := " "
		textStyle := lipgloss.NewStyle()

		if i == m.messageIndex {
			markerColor := mutedColor
			if focused {
				markerColor = m.accountColor(message.Account)
			}

			marker = m.style(markerColor).Render("▌")
			textStyle = textStyle.Foreground(m.paint(markerColor))
		}

		unread := " "
		if message.Unread {
			unread = m.style(m.accountColor(message.Account)).Render("●")
			textStyle = textStyle.Bold(true)
		}

		subject := message.Subject
		if subject == "" {
			subject = "(no subject)"
		}

		attachmentMark := ""
		if len(message.Attachments) > 0 {
			attachmentMark = " " + m.style(mutedColor).Render("◈")
		}

		subjectText := textStyle.Render(ansi.Truncate(subject, max(1, subjectWidth-2), "…")) + attachmentMark

		line := marker + unread + " " + fit(textStyle.Render(ansi.Truncate(message.From.Short(), fromWidth, "…")), fromWidth) + "  " + fit(subjectText, subjectWidth)

		if showFolder {
			line += "  " + fit(m.style(mutedColor).Render(message.Folder), folderWidth)
		}

		if showAccount {
			line += "  " + fit(m.style(m.accountColor(message.Account)).Render(message.Account), accountWidth)
		}

		date := message.Date.Format("Jan 06")

		switch {
		case message.Date.Year() == now.Year() && message.Date.YearDay() == now.YearDay():
			date = message.Date.Format("15:04")

		case now.Sub(message.Date) < 6*24*time.Hour:
			date = message.Date.Format("Mon")

		case message.Date.Year() == now.Year():
			date = message.Date.Format("2 Jan")
		}

		line += "  " + lipgloss.PlaceHorizontal(dateWidth, lipgloss.Right, m.style(mutedColor).Render(date))

		lines = append(lines, m.zones.Mark(fmt.Sprintf("message:%d", i), fit(line, innerWidth)))
	}

	borderColor := mutedColor
	if focused {
		borderColor = accentColor
	}

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.paint(borderColor)).Width(innerWidth + 2).Height(innerHeight + 2)

	return m.zones.Mark("pane:list", box.Render(strings.Join(lines, "\n")))
}

func (m *Model) draftView(innerWidth, innerHeight int) string {
	draft := m.draft
	labelStyle := m.style(mutedColor).Width(10)

	lines := []string{
		" " + boldStyle.Render("Draft") + m.style(mutedColor).Render("  from ") + m.style(m.accountColor(draft.Account)).Bold(true).Render(draft.Account),
		"",
		" " + labelStyle.Render("To") + fit(draft.To, innerWidth-12),
		" " + labelStyle.Render("Subject") + fit(draft.Subject, innerWidth-12),
	}

	for i, path := range draft.Attach {
		label := ""
		if i == 0 {
			label = "Attach"
		}

		status := ""
		if _, err := os.Stat(expandHome(path)); err != nil {
			status = m.style(redColor).Render("  not found")
		}

		lines = append(lines, " "+labelStyle.Render(label)+path+status)
	}

	lines = append(lines, " "+m.style(mutedColor).Render(strings.Repeat("─", innerWidth-2)))

	footerLines := 0
	if m.draftError != "" {
		footerLines = 2
	}

	if m.sending || m.delivering {
		footerLines = 3
	}

	bodyHeight := max(0, innerHeight-len(lines)-footerLines)
	body := lipgloss.NewStyle().Width(innerWidth - 2).Render(draft.Body)
	bodyLines := strings.Split(body, "\n")

	for _, line := range bodyLines[:min(len(bodyLines), bodyHeight)] {
		lines = append(lines, " "+line)
	}

	for len(lines) < innerHeight-footerLines {
		lines = append(lines, "")
	}

	switch {
	case m.delivering:
		lines = append(lines, "", " "+m.style(yellowColor).Render("Sending..."), "")

	case m.sending:
		barWidth := min(40, innerWidth-4)
		filled := barWidth * (sendDelay - m.sendRemaining) / sendDelay
		bar := m.style(yellowColor).Render(strings.Repeat("━", filled)) + m.style(mutedColor).Render(strings.Repeat("━", barWidth-filled))
		lines = append(lines, "", " "+m.style(yellowColor).Bold(true).Render(fmt.Sprintf("Sending in %ds", m.sendRemaining))+"   "+m.button("u", "undo"), " "+bar)

	case m.draftError != "":
		lines = append(lines, "", " "+m.style(redColor).Render(ansi.Truncate(m.draftError, innerWidth-2, "…")))
	}

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.paint(m.accountColor(draft.Account))).Width(innerWidth + 2).Height(innerHeight + 2)

	return box.Render(strings.Join(lines[:min(len(lines), innerHeight)], "\n"))
}

func (m *Model) footer() string {
	var buttons []string

	switch {
	case m.popup == movePopup:
		buttons = []string{m.button("enter", "move"), m.button("esc", "cancel")}

	case m.popup == discardPopup:
		buttons = []string{m.button("y", "discard"), m.button("n", "keep")}

	case m.popup == logPopup:
		buttons = []string{m.button("esc", "close")}

	case m.sending:
		buttons = []string{m.button("u", "undo send")}

	case m.delivering:
		return ""

	case m.draftVisible:
		buttons = []string{m.button("e", "edit"), m.button("s", "send"), m.button("h", "hide"), m.button("x", "discard")}

	case m.searchFocused:
		buttons = []string{m.button("enter", "browse results"), m.button("esc", "clear search")}

	default:
		buttons = []string{
			m.button("A", "archive"), m.button("M", "move"), m.button("!", "spam"), m.button("D", "delete"),
			m.button("r", "reply"), m.button("n", "new"), m.button("/", "search"), m.button("ctrl+r", "refresh"),
			m.button("L", "log"), m.button("q", "quit"),
		}
	}

	textWidth := 0
	for _, button := range buttons {
		textWidth += lipgloss.Width(button)
	}

	gaps := max(1, len(buttons)-1)
	base, extra := 1, 0

	if spacing := m.width - 2 - textWidth; spacing > gaps {
		base, extra = spacing/gaps, spacing%gaps
	}

	var bar strings.Builder

	for i, button := range buttons {
		bar.WriteString(button)

		if i < len(buttons)-1 {
			width := base
			if i < extra {
				width++
			}

			bar.WriteString(strings.Repeat(" ", width))
		}
	}

	return " " + ansi.Truncate(bar.String(), m.width-2, "")
}

func (m *Model) accountColor(name string) adaptiveColor {
	index := slices.IndexFunc(m.accounts, func(account mailbox.Account) bool { return account.Name == name })
	if index < 0 {
		return accentColor
	}

	return accountColors[index%len(accountColors)]
}

func (m *Model) paint(colour adaptiveColor) color.Color {
	if m.lightBackground {
		return lipgloss.Color(colour.Light)
	}

	return lipgloss.Color(colour.Dark)
}

func (m *Model) style(colour adaptiveColor) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(m.paint(colour))
}

func (m *Model) button(key, label string) string {
	return m.zones.Mark("key:"+key, m.style(mutedColor).Bold(true).Render(key)+" "+m.style(mutedColor).Render(label))
}

func (m *Model) popupBox(lines []string, width int) string {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.paint(accentColor)).Padding(0, 1).Width(width + 2).Render(strings.Join(lines, "\n"))
}

func overlay(background, box string) string {
	backgroundLines := strings.Split(background, "\n")
	boxLines := strings.Split(box, "\n")

	backgroundWidth := lipgloss.Width(background)
	boxWidth := lipgloss.Width(box)

	left := max(0, (backgroundWidth-boxWidth)/2)
	top := max(0, (len(backgroundLines)-len(boxLines))/2)

	for i, boxLine := range boxLines {
		row := top + i
		if row >= len(backgroundLines) {
			break
		}

		line := backgroundLines[row]
		backgroundLines[row] = ansi.Truncate(line, left, "") + boxLine + ansi.TruncateLeft(line, left+boxWidth, "")
	}

	return strings.Join(backgroundLines, "\n")
}

func fit(text string, width int) string {
	if width <= 0 {
		return ""
	}

	truncated := ansi.Truncate(text, width, "…")

	return truncated + strings.Repeat(" ", max(0, width-lipgloss.Width(truncated)))
}

func expandHome(path string) string {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path
	}

	home, err := os.UserHomeDir()

	if err != nil {
		return path
	}

	return filepath.Join(home, rest)
}

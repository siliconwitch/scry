package tui

import (
	"cmp"
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
	emptyTrashPopup
	logPopup
)

type folderRow struct {
	account string
	folder  string
}

type syncDoneMsg struct {
	account string
	err     error
}

type syncProgressMsg struct{}

type retryMsg struct {
	account    string
	generation int
}

type heartbeatMsg struct{ scheduled time.Time }

type watchRetryMsg struct {
	account    string
	generation int
}

type watchDoneMsg struct {
	account    string
	generation int
	err        error
}

type logTickMsg struct{ generation int }

type queuedMsg struct {
	account string
	err     error
}

type unqueuedMsg struct {
	draft mailbox.Draft
	err   error
}

type savedMsg struct{ err error }

type bodyLoadedMsg struct {
	id      string
	message mailbox.Message
	err     error
}

type sendTickMsg struct{ generation int }

type noticeExpiredMsg struct{ generation int }

type editorDoneMsg struct {
	path string
	err  error
}

const sendDelay = 5

const folderPaneWidth = 24

const heartbeatInterval = 15 * time.Second

type accountSync struct {
	running         bool
	queued          bool
	failed          bool
	cancel          context.CancelFunc
	startedAt       time.Time
	retryDelay      time.Duration
	retryGeneration int
	watching        bool
	watchGeneration int
	watchRetryDelay time.Duration
	stopWatch       context.CancelFunc
}

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
		{Light: "#0F766E", Dark: "#2DD4BF"},
		{Light: "#4D7C0F", Dark: "#A3E635"},
		{Light: "#4338CA", Dark: "#A5B4FC"},
		{Light: "#A21CAF", Dark: "#E879F9"},
	}

	boldStyle = lipgloss.NewStyle().Bold(true)
)

var clickableKeys = []string{"A", "M", "!", "D", "E", "r", "R", "n", "ctrl+r", "L", "q", "ctrl+c", "/", "e", "s", "h", "l", "x", "u", "w", "y", "enter", "esc", "j", "k", "down", "up", "pgdown", "pgup", "home", "end", "1", "2", "3", "4", "5", "6", "7", "8", "9"}

// How the footer writes key names that aren't a single character.
var keyNames = map[string]string{"down": "↓", "up": "↑", "left": "←", "right": "→", "pgdown": "pgdn"}

type Model struct {
	options Options
	zones   *zone.Manager

	width           int
	height          int
	lightBackground bool

	accounts     []mailbox.Account
	folderRows   []folderRow
	unreadCounts []int
	folderIndex  int
	messages     []mailbox.Message
	messageIndex int
	listOffset   int
	folderOffset int
	focus        pane

	search        textinput.Model
	searchFocused bool

	shownID string

	syncs map[string]*accountSync

	draft              *mailbox.Draft
	draftVisible       bool
	draftError         string
	missingAttachments []string
	sending            bool
	delivering         bool
	sendRemaining      int
	sendGen            int

	popup           popup
	moveTargets     []string
	moveIndex       int
	logLines        []synclog.Entry
	logOffset       int
	logFilter       string
	logGeneration   int
	pendingCompose  bool
	pendingReplyID  string
	pendingReplyAll bool
	pendingUnqueue  string

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
		syncs:    map[string]*accountSync{},
	}

	for _, account := range m.accounts {
		m.syncs[account.Name] = &accountSync{}
	}

	m.reload()

	return m
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, m.showSelected(false), heartbeat()}

	for _, account := range m.accounts {
		cmds = append(cmds, m.requestSync(account.Name))
	}

	return tea.Batch(cmds...)
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
		if !m.searchFocused || m.popup != noPopup {
			return m.handleKey(msg.String())
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "esc":
			m.search.SetValue("")
			m.search.Blur()
			m.searchFocused = false
			m.messageIndex = -1
			m.listOffset = 0
			m.reload()
			return m, m.showSelected(false)

		case "enter", "tab":
			m.search.Blur()
			m.searchFocused = false
			m.focus = listPane
			return m, nil

		case "up":
			m.focus = listPane
			return m.handleKey("k")

		case "down":
			m.focus = listPane
			return m.handleKey("j")
		}

		previous := m.search.Value()

		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)

		if m.search.Value() != previous {
			m.messageIndex = -1
			m.listOffset = 0
			m.reload()
			cmd = tea.Batch(cmd, m.showSelected(false))
		}

		return m, cmd

	case tea.MouseWheelMsg:
		key, step := "j", 1
		if msg.Mouse().Button == tea.MouseWheelUp {
			key, step = "k", -1
		}

		switch {
		case m.popup != noPopup:
			return m.handleKey(key)

		case m.zones.Get("pane:folders").InBounds(msg):
			folderLines := len(m.folderRows) + 2*len(m.accounts)
			m.folderOffset = max(0, min(m.folderOffset+step, folderLines-(m.panesHeight()-2)))

		case m.zones.Get("pane:list").InBounds(msg):
			m.listOffset = max(0, min(m.listOffset+step, len(m.messages)-m.listRows()))
		}

		return m, nil

	case tea.MouseClickMsg:
		if msg.Mouse().Button != tea.MouseLeft {
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
				return m, m.selectFolder(i)
			}
		}

		for i := m.listOffset; i < min(len(m.messages), m.listOffset+m.listRows()); i++ {
			if m.zones.Get(fmt.Sprintf("message:%d", i)).InBounds(msg) {
				m.focus = listPane
				m.messageIndex = i
				return m, m.showSelected(true)
			}
		}

		return m, nil

	case syncProgressMsg:
		if !m.syncing() {
			return m, nil
		}

		m.reload()

		return m, tea.Batch(m.showSelected(false), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return syncProgressMsg{} }))

	case syncDoneMsg:
		state := m.syncs[msg.account]
		state.running = false
		state.failed = msg.err != nil

		if msg.err != nil {
			for _, line := range strings.Split(msg.err.Error(), "\n") {
				m.options.Log.Write(msg.account, "Sync failed: %s", line)
			}
		} else {
			m.options.Log.Write(msg.account, "Sync finished")
			state.retryDelay = 0
		}

		m.reload()

		cmds := []tea.Cmd{m.showSelected(false)}

		unlocked := m.options.Mail.Unlocked(msg.account)

		if !state.watching && unlocked {
			cmds = append(cmds, m.startWatch(msg.account))
		}

		switch {
		case state.queued:
			state.queued = false
			m.options.Log.Write(msg.account, "Starting the queued sync")
			cmds = append(cmds, m.requestSync(msg.account))

		case msg.err != nil && !unlocked:
			m.options.Log.Write(msg.account, "Not retrying, because the account isn't unlocked. Press Ctrl+R to try again")

		case msg.err != nil:
			state.retryDelay = min(5*time.Minute, max(10*time.Second, 2*state.retryDelay))
			state.retryGeneration++

			account := msg.account
			generation := state.retryGeneration

			m.options.Log.Write(account, "Retrying in %s", state.retryDelay)
			cmds = append(cmds, tea.Tick(state.retryDelay, func(time.Time) tea.Msg { return retryMsg{account: account, generation: generation} }))
		}

		return m, tea.Batch(cmds...)

	case retryMsg:
		if msg.generation != m.syncs[msg.account].retryGeneration {
			return m, nil
		}

		return m, m.requestSync(msg.account)

	case watchDoneMsg:
		state := m.syncs[msg.account]

		if msg.generation != state.watchGeneration {
			return m, nil
		}

		state.watching = false

		if msg.err != nil {
			state.watchRetryDelay = min(5*time.Minute, max(10*time.Second, 2*state.watchRetryDelay))

			account := msg.account
			generation := state.watchGeneration

			m.options.Log.Write(account, "Inbox watch stopped: %s. Reconnecting in %s", msg.err, state.watchRetryDelay)

			return m, tea.Tick(state.watchRetryDelay, func(time.Time) tea.Msg { return watchRetryMsg{account: account, generation: generation} })
		}

		state.watchRetryDelay = 0

		return m, tea.Batch(m.startWatch(msg.account), m.requestSync(msg.account))

	case watchRetryMsg:
		if msg.generation != m.syncs[msg.account].watchGeneration {
			return m, nil
		}

		return m, tea.Batch(m.startWatch(msg.account), m.requestSync(msg.account))

	case heartbeatMsg:
		now := time.Now().Round(0)
		asleep := now.Sub(msg.scheduled) - heartbeatInterval
		woke := asleep > 30*time.Second

		cmds := []tea.Cmd{heartbeat()}

		if woke {
			m.options.Log.Write("", "Woke after %s asleep, so every account reconnects", asleep.Round(time.Second))
		}

		for _, account := range m.accounts {
			state := m.syncs[account.Name]

			if woke && state.stopWatch != nil {
				state.stopWatch()
				state.watching = false
				state.watchGeneration++
			}

			switch {
			case !m.options.Mail.Unlocked(account.Name):

			case woke && state.running:
				state.cancel()
				state.queued = true

			case woke:
				cmds = append(cmds, m.requestSync(account.Name))

			case !state.running && now.Sub(state.startedAt) >= 10*time.Minute:
				m.options.Log.Write(account.Name, "Ten minutes since the last sync")
				cmds = append(cmds, m.requestSync(account.Name))
			}
		}

		return m, tea.Batch(cmds...)

	case logTickMsg:
		if msg.generation != m.logGeneration || m.popup != logPopup {
			return m, nil
		}

		rows := m.logRows()
		following := m.logOffset >= len(m.logEntries())-rows

		m.logLines = m.options.Log.Entries()

		if following {
			m.logOffset = max(0, len(m.logEntries())-rows)
		}

		generation := msg.generation

		return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return logTickMsg{generation: generation} })

	case savedMsg:
		if msg.err == nil {
			return m, nil
		}

		m.options.Log.Write("", "Save failed: %s", msg.err)

		return m, m.setNotice(msg.err.Error())

	case bodyLoadedMsg:
		if msg.id != m.shownID {
			return m, nil
		}

		recipients := []viewer.Address{}
		for _, recipient := range msg.message.To {
			recipients = append(recipients, viewer.Address{Name: recipient.Name, Email: recipient.Email})
		}

		var copied, blindCopied []viewer.Address
		for _, recipient := range msg.message.Cc {
			copied = append(copied, viewer.Address{Name: recipient.Name, Email: recipient.Email})
		}

		for _, recipient := range msg.message.Bcc {
			blindCopied = append(blindCopied, viewer.Address{Name: recipient.Name, Email: recipient.Email})
		}

		var attachments []viewer.Attachment
		for _, attachment := range msg.message.Attachments {
			attachments = append(attachments, viewer.Attachment{Name: attachment.Name, Data: attachment.Data})
		}

		m.options.Viewer.Show(viewer.Page{
			ID:           msg.message.ID,
			Subject:      msg.message.Subject,
			From:         viewer.Address{Name: msg.message.From.Name, Email: msg.message.From.Email},
			To:           recipients,
			Cc:           copied,
			Bcc:          blindCopied,
			Date:         msg.message.Date.Local().Format("Mon 2 Jan 2006, 15:04"),
			HTML:         msg.message.HTML,
			Text:         msg.message.Text,
			Attachments:  attachments,
			AccountLight: m.accountColor(msg.message.Account).Light,
			AccountDark:  m.accountColor(msg.message.Account).Dark,
		})

		if msg.err != nil {
			return m, m.setNotice("Can't open this email: " + msg.err.Error())
		}

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

		draft := *m.draft
		draft.Attach = nil

		for _, path := range m.draft.Attach {
			draft.Attach = append(draft.Attach, expandHome(path))
		}

		mail := m.options.Mail

		return m, func() tea.Msg { return queuedMsg{account: draft.Account, err: mail.Queue(draft)} }

	case queuedMsg:
		m.delivering = false

		if msg.err != nil {
			m.options.Log.Write(msg.account, "Adding an email to the Outbox failed. The draft shows why")
			m.draftError = "Couldn't send: " + msg.err.Error()
			m.draftVisible = true
			return m, nil
		}

		m.options.Log.Write(msg.account, "Added an email to the Outbox")
		m.draft = nil
		m.draftVisible = false
		m.reload()

		return m, tea.Batch(m.setNotice("Sending from "+msg.account), m.requestSync(msg.account))

	case unqueuedMsg:
		if msg.err != nil {
			return m, m.setNotice("Can't return it to the draft: " + msg.err.Error())
		}

		m.options.Log.Write(msg.draft.Account, "Returned an email from the Outbox to the draft")
		m.draft = &msg.draft
		m.draftVisible = true
		m.draftError = ""
		m.missingAttachments = nil
		m.reload()

		return m, m.showSelected(false)

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

			case "cc":
				edited.Cc = value

			case "bcc":
				edited.Bcc = value

			case "subject":
				edited.Subject = value

			case "attach":
				if value != "" {
					edited.Attach = append(edited.Attach, value)
				}

			default:
				m.draftError = fmt.Sprintf("Unknown header line %q. Use From, To, Cc, Bcc, Subject or Attach.", line)
			}
		}

		_, ok := m.options.Mail.Account(edited.Account)

		if !ok && m.draftError == "" {
			var names []string
			for _, account := range m.accounts {
				names = append(names, account.Name)
			}

			m.draftError = fmt.Sprintf("No account called %q. Use one of: %s.", edited.Account, strings.Join(names, ", "))
		}

		if strings.TrimSpace(edited.To) == "" && m.draftError == "" {
			m.draftError = "Add a recipient on the To line before sending."
		}

		m.missingAttachments = nil

		for _, path := range edited.Attach {
			_, err := os.Stat(expandHome(path))

			if err != nil {
				m.missingAttachments = append(m.missingAttachments, path)
			}
		}

		m.draft = &edited
		m.draftVisible = true

		return m, nil
	}

	return m, nil
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

			if m.pendingUnqueue != "" {
				id := m.pendingUnqueue
				m.pendingUnqueue = ""
				return m, m.unqueue(id)
			}

			if m.pendingCompose {
				return m, m.startCompose(m.pendingReplyID, m.pendingReplyAll)
			}

			return m, m.setNotice("Draft discarded")

		case "n", "esc", "q":
			m.popup = noPopup
			m.pendingUnqueue = ""
		}

		return m, nil

	case emptyTrashPopup:
		switch key {
		case "y":
			m.popup = noPopup
			account := m.folderRows[m.folderIndex].account
			emptied := m.options.Mail.EmptyTrash(account)

			m.options.Log.Write(account, "Emptied Trash: %d emails to delete for good", emptied)

			m.messageIndex = -1
			m.reload()

			mail := m.options.Mail

			return m, tea.Batch(m.showSelected(false), m.setNotice("Emptied Trash"), func() tea.Msg { return savedMsg{err: mail.Save()} }, m.requestSync(account))

		case "n", "esc", "q":
			m.popup = noPopup
		}

		return m, nil

	case logPopup:
		visibleLines := m.logRows()

		switch key {
		case "j", "down", "k", "up", "pgdown", "pgup", "home", "end":
			step := map[string]int{"j": 1, "down": 1, "k": -1, "up": -1, "pgdown": visibleLines, "pgup": -visibleLines, "home": -1 << 30, "end": 1 << 30}[key]

			m.logOffset = max(0, min(m.logOffset+step, len(m.logEntries())-visibleLines))

		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			index := int(key[0] - '1')

			if index >= len(m.accounts) {
				return m, nil
			}

			if m.logFilter == m.accounts[index].Name {
				m.logFilter = ""
			} else {
				m.logFilter = m.accounts[index].Name
			}

			m.logOffset = max(0, len(m.logEntries())-visibleLines)

		case "esc", "q", "L":
			m.popup = noPopup
		}

		return m, nil
	}

	if m.delivering {
		return m, nil
	}

	if m.sending {
		if key == "u" {
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
		var cmds []tea.Cmd

		for _, account := range m.accounts {
			cmds = append(cmds, m.requestSync(account.Name))
		}

		return m, tea.Batch(cmds...)

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		index := int(key[0] - '1')

		if index >= len(m.accounts) {
			return m, nil
		}

		m.draftVisible = false
		m.focus = listPane

		return m, m.selectFolder(slices.Index(m.folderRows, folderRow{account: m.accounts[index].Name, folder: mailbox.Inbox}))

	case "L":
		m.logLines = m.options.Log.Entries()
		m.logOffset = max(0, len(m.logEntries())-m.logRows())
		m.popup = logPopup
		m.logGeneration++

		generation := m.logGeneration

		return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return logTickMsg{generation: generation} })

	case "w":
		if m.draft != nil {
			m.draftVisible = true
		}

		return m, nil

	case "n":
		return m, m.requestCompose("", false)

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
				_, err := os.Stat(expandHome(path))

				if err != nil {
					m.draftError = "Can't find the attachment " + path + "."
					return m, nil
				}
			}

			m.sending = true
			m.sendRemaining = sendDelay
			m.sendGen++

			return m, m.sendTick()

		case "h":
			m.draftVisible = false

		case "x":
			m.pendingCompose = false
			m.pendingUnqueue = ""
			m.popup = discardPopup
		}

		return m, nil
	}

	selected, hasSelection := m.selected()

	switch key {
	case "h", "left":
		m.focus = folderPane

	case "tab", "enter", "l", "right":
		if key == "tab" && m.focus == listPane {
			m.focus = folderPane
			return m, nil
		}

		m.focus = listPane
		m.messageIndex = min(max(0, m.messageIndex), len(m.messages)-1)
		m.clampList()

		return m, m.showSelected(true)

	case "j", "down", "k", "up", "pgdown", "pgup", "home", "end":
		step := map[string]int{"j": 1, "down": 1, "k": -1, "up": -1, "pgdown": m.listRows(), "pgup": -m.listRows(), "home": -1 << 30, "end": 1 << 30}[key]

		if m.focus == folderPane {
			return m, m.selectFolder(m.folderIndex + step)
		}

		previous := m.messageIndex
		m.messageIndex = min(max(0, m.messageIndex+step), len(m.messages)-1)
		m.clampList()
		return m, m.showSelected(m.messageIndex != previous)

	case "esc":
		if m.search.Value() != "" {
			m.search.SetValue("")
			m.messageIndex = -1
			m.reload()
			return m, m.showSelected(false)
		}

	case "A":
		return m, m.moveSelected(mailbox.Archive)

	case "!":
		if hasSelection && selected.Folder == mailbox.Spam {
			return m, m.moveSelected(mailbox.Inbox)
		}

		return m, m.moveSelected(mailbox.Spam)

	case "E":
		row := m.folderRows[m.folderIndex]

		if m.search.Value() == "" && row.account != "" && row.folder == mailbox.Trash && len(m.messages) > 0 {
			m.popup = emptyTrashPopup
		}

	case "D":
		if !hasSelection || selected.Folder != mailbox.Outbox {
			return m, m.moveSelected(mailbox.Trash)
		}

		if m.draft != nil {
			m.pendingCompose = false
			m.pendingUnqueue = selected.ID
			m.popup = discardPopup
			return m, nil
		}

		return m, m.unqueue(selected.ID)

	case "M":
		if !hasSelection {
			return m, nil
		}

		if selected.Folder == mailbox.Outbox {
			return m, m.setNotice(mailbox.ErrInOutbox.Error())
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
			return m, m.requestCompose(selected.ID, true)
		}

	case "R":
		if hasSelection {
			return m, m.requestCompose(selected.ID, false)
		}

	case "y":
		if !hasSelection {
			return m, nil
		}

		return m, tea.Batch(tea.SetClipboard(selected.From.Email), m.setNotice("Copied "+selected.From.Email))
	}

	return m, nil
}

func (m *Model) selectFolder(index int) tea.Cmd {
	m.folderIndex = max(0, min(index, len(m.folderRows)-1))

	if m.search.Value() != "" {
		m.search.SetValue("")
	}

	m.messageIndex = -1
	m.listOffset = 0
	m.reload()
	m.messageIndex = min(0, len(m.messages)-1)

	selectedLine := m.folderIndex
	previousAccount := ""

	for _, row := range m.folderRows[:m.folderIndex+1] {
		if row.account != previousAccount {
			selectedLine += 2
			previousAccount = row.account
		}
	}

	folderRows := m.panesHeight() - 2
	m.folderOffset = max(0, min(selectedLine-folderRows/2, len(m.folderRows)+2*len(m.accounts)-folderRows))

	return m.showSelected(m.focus == listPane)
}

func (m *Model) reload() {
	previous, hadSelection := m.selected()
	selectionVisible := hadSelection && m.messageIndex >= m.listOffset && m.messageIndex < m.listOffset+m.listRows()

	selectedRow := folderRow{folder: mailbox.Inbox}

	if m.folderIndex < len(m.folderRows) {
		selectedRow = m.folderRows[m.folderIndex]
	}

	m.folderRows = []folderRow{{folder: mailbox.Inbox}}

	for _, account := range m.accounts {
		for _, folder := range account.Folders {
			m.folderRows = append(m.folderRows, folderRow{account: account.Name, folder: folder})

			if folder == mailbox.Inbox && len(m.options.Mail.Folder(account.Name, mailbox.Outbox)) > 0 {
				m.folderRows = append(m.folderRows, folderRow{account: account.Name, folder: mailbox.Outbox})
			}
		}
	}

	m.folderIndex = slices.Index(m.folderRows, selectedRow)

	if m.folderIndex < 0 {
		m.folderIndex = slices.Index(m.folderRows, folderRow{account: selectedRow.account, folder: mailbox.Inbox})
	}

	query := strings.TrimSpace(m.search.Value())

	if query != "" {
		m.messages = mailbox.Search(m.options.Mail.All(), query)
	} else {
		row := m.folderRows[m.folderIndex]
		m.messages = m.options.Mail.Folder(row.account, row.folder)
	}

	m.messageIndex = slices.IndexFunc(m.messages, func(message mailbox.Message) bool { return message.ID == previous.ID })

	switch {
	case selectionVisible && m.messageIndex >= 0:
		m.clampList()

	default:
		m.listOffset = max(0, min(m.listOffset, len(m.messages)-m.listRows()))
	}

	m.unreadCounts = make([]int, len(m.folderRows))

	for i, row := range m.folderRows {
		m.unreadCounts[i] = m.options.Mail.Unread(row.account, row.folder)
	}
}

func (m *Model) selected() (mailbox.Message, bool) {
	if m.messageIndex < 0 || m.messageIndex >= len(m.messages) {
		return mailbox.Message{}, false
	}

	return m.messages[m.messageIndex], true
}

func (m *Model) showSelected(markRead bool) tea.Cmd {
	message, ok := m.selected()

	if !ok {
		if m.shownID != "" {
			m.options.Viewer.Show(viewer.Page{})
			m.shownID = ""
		}

		return nil
	}

	mail := m.options.Mail

	var cmds []tea.Cmd

	if message.ID != m.shownID {
		m.shownID = message.ID

		cmds = append(cmds, func() tea.Msg {
			loaded, err := mail.Load(message.ID)
			return bodyLoadedMsg{id: message.ID, message: loaded, err: err}
		})
	}

	if markRead && message.Unread {
		mail.MarkRead(message.ID)
		m.reload()

		cmds = append(cmds, func() tea.Msg { return savedMsg{err: mail.Save()} }, m.requestSync(message.Account))
	}

	return tea.Batch(cmds...)
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

	m.options.Log.Write(message.Account, "Moved an email to %s", folder)

	index := m.messageIndex
	m.reload()
	m.messageIndex = min(index, len(m.messages)-1)
	m.clampList()

	mail := m.options.Mail

	return tea.Batch(m.showSelected(true), m.setNotice("Moved to "+folder), func() tea.Msg { return savedMsg{err: mail.Save()} }, m.requestSync(message.Account))
}

func (m *Model) unqueue(id string) tea.Cmd {
	mail := m.options.Mail

	return func() tea.Msg {
		draft, err := mail.Unqueue(id)

		return unqueuedMsg{draft: draft, err: err}
	}
}

func (m *Model) requestCompose(replyID string, replyAll bool) tea.Cmd {
	if m.draft != nil {
		m.pendingUnqueue = ""
		m.pendingCompose = true
		m.pendingReplyID = replyID
		m.pendingReplyAll = replyAll
		m.popup = discardPopup
		return nil
	}

	return m.startCompose(replyID, replyAll)
}

func (m *Model) startCompose(replyID string, replyAll bool) tea.Cmd {
	draft := mailbox.Draft{Account: m.accounts[0].Name}

	row := m.folderRows[m.folderIndex]

	if row.account != "" {
		draft.Account = row.account
	}

	original, ok := m.options.Mail.Get(replyID)

	if ok {
		subject := original.Subject

		if !strings.HasPrefix(strings.ToLower(subject), "re:") {
			subject = "Re: " + subject
		}

		var quoted strings.Builder
		for _, line := range strings.Split(original.Text, "\n") {
			quoted.WriteString("> " + line + "\n")
		}

		recipients := []string{original.From.Email}
		var copied []string

		if replyAll {
			account, _ := m.options.Mail.Account(original.Account)
			added := []string{account.Email}

			recipients = nil

			for _, group := range []struct {
				addresses []mailbox.Address
				line      *[]string
			}{{append([]mailbox.Address{original.From}, original.To...), &recipients}, {original.Cc, &copied}} {
				for _, address := range group.addresses {
					if slices.ContainsFunc(added, func(email string) bool { return strings.EqualFold(email, address.Email) }) {
						continue
					}

					added = append(added, address.Email)
					*group.line = append(*group.line, address.Email)
				}
			}
		}

		draft = mailbox.Draft{
			Account: original.Account,
			To:      strings.Join(recipients, ", "),
			Cc:      strings.Join(copied, ", "),
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
	content.WriteString("Cc: " + m.draft.Cc + "\n")
	content.WriteString("Bcc: " + m.draft.Bcc + "\n")
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

	words := strings.Fields(editor)

	if len(words) == 0 {
		words = []string{"vi"}
	}

	path := file.Name()

	command := exec.Command(words[0], append(words[1:], path)...)

	return tea.ExecProcess(command, func(err error) tea.Msg { return editorDoneMsg{path: path, err: err} })
}

func (m *Model) requestSync(account string) tea.Cmd {
	state := m.syncs[account]

	if state.running {
		if !state.queued {
			m.options.Log.Write(account, "Sync queued behind the running one")
		}

		state.queued = true

		return nil
	}

	var progressTick tea.Cmd

	if !m.syncing() {
		progressTick = tea.Tick(2*time.Second, func(time.Time) tea.Msg { return syncProgressMsg{} })
	}

	ctx, cancel := context.WithCancel(context.Background())

	state.running = true
	state.cancel = cancel
	state.startedAt = time.Now().Round(0)
	state.retryGeneration++

	m.options.Log.Write(account, "Sync started")

	mail := m.options.Mail
	log := m.options.Log

	sync := func() tea.Msg {
		defer cancel()

		err := mail.Sync(ctx, account, func(format string, arguments ...any) { log.Write(account, format, arguments...) })

		return syncDoneMsg{account: account, err: err}
	}

	return tea.Batch(sync, progressTick)
}

func (m *Model) startWatch(account string) tea.Cmd {
	state := m.syncs[account]

	if state.stopWatch != nil {
		state.stopWatch()
	}

	ctx, cancel := context.WithCancel(context.Background())

	state.watching = true
	state.watchGeneration++
	state.stopWatch = cancel

	generation := state.watchGeneration
	mail := m.options.Mail
	log := m.options.Log

	return func() tea.Msg {
		defer cancel()

		err := mail.WatchInbox(ctx, account, func(format string, arguments ...any) { log.Write(account, format, arguments...) })

		return watchDoneMsg{account: account, generation: generation, err: err}
	}
}

func (m *Model) syncing() bool {
	for _, state := range m.syncs {
		if state.running {
			return true
		}
	}

	return false
}

func heartbeat() tea.Cmd {
	scheduled := time.Now().Round(0)

	return tea.Tick(heartbeatInterval, func(time.Time) tea.Msg { return heartbeatMsg{scheduled: scheduled} })
}

func (m *Model) logEntries() []synclog.Entry {
	if m.logFilter == "" {
		return m.logLines
	}

	var filtered []synclog.Entry

	for _, entry := range m.logLines {
		if entry.Account == m.logFilter {
			filtered = append(filtered, entry)
		}
	}

	return filtered
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
	return max(3, m.height-1-len(m.footer()))
}

func (m *Model) logRows() int {
	return max(1, min(12, m.panesHeight()-6))
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

	failed := false
	done, total := 0, 0

	for name, state := range m.syncs {
		failed = failed || state.failed

		if state.running {
			accountDone, accountTotal := m.options.Mail.SyncProgress(name)
			done += accountDone
			total += accountTotal
		}
	}

	statusColor := accentColor
	switch {
	case m.syncing():
		statusColor = yellowColor

	case failed:
		statusColor = redColor
	}

	logo := m.style(statusColor).Render("◉") + m.style(accentColor).Bold(true).Render(" scry ")

	if m.syncing() && total > 0 {
		logo += m.style(yellowColor).Render(fmt.Sprintf("%d%% ", done*100/total))
	}

	logo = " " + m.zones.Mark("key:L", logo)

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

	draftButton := ""
	if m.draft != nil && !m.draftVisible {
		draftButton = m.button("w", "draft") + " "
	}

	notice := m.notice
	if m.sending {
		notice = fmt.Sprintf("Sending in %ds. Press u to undo.", m.sendRemaining)
	}

	if m.delivering {
		notice = "Sending..."
	}

	noticeWidth := m.width - lipgloss.Width(logo) - searchWidth - lipgloss.Width(draftButton) - 4
	noticeText := lipgloss.PlaceHorizontal(max(0, noticeWidth), lipgloss.Right, m.style(mutedColor).Render(ansi.Truncate(notice, max(0, noticeWidth), "…")))

	topBar := logo + " " + searchBox + "  " + noticeText + " " + draftButton

	innerHeight := m.panesHeight() - 2
	folderInnerWidth := folderPaneWidth - 2

	var folderLines []string
	previousAccount := "-"

	for i, row := range m.folderRows {
		if row.account != previousAccount && row.account != "" {
			folderLines = append(folderLines, "", " "+m.style(m.accountColor(row.account)).Bold(true).Render(row.account))
		}

		previousAccount = row.account

		label := row.folder
		if row.account == "" {
			label = "All inboxes"
		}

		count := ""
		if m.unreadCounts[i] > 0 {
			count = strconv.Itoa(m.unreadCounts[i])
		}

		indent := "  "
		if row.account == "" {
			indent = " "
		}

		text := indent + label
		gap := max(1, folderInnerWidth-2-lipgloss.Width(text)-len(count))

		marker := "  "
		style := lipgloss.NewStyle()

		if i == m.folderIndex && m.search.Value() == "" {
			markerColor := mutedColor
			if m.focus == folderPane && !m.draftVisible {
				markerColor = m.accountColor(row.account)
			}

			marker = m.style(markerColor).Render("▌") + " "
			style = style.Foreground(m.paint(markerColor)).Bold(true)
		}

		line := marker + style.Render(text) + strings.Repeat(" ", gap) + m.style(m.accountColor(row.account)).Render(count)

		folderLines = append(folderLines, m.zones.Mark(fmt.Sprintf("folder:%d", i), fit(line, folderInnerWidth)))
	}

	folderOffset := max(0, min(m.folderOffset, len(folderLines)-innerHeight))
	visibleFolders := folderLines[folderOffset:min(len(folderLines), folderOffset+innerHeight)]

	folderBorder := mutedColor
	if m.focus == folderPane && !m.draftVisible && !m.searchFocused {
		folderBorder = accentColor
	}

	folderBox := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.paint(folderBorder)).Width(folderInnerWidth + 2).Height(innerHeight + 2)
	folders := m.zones.Mark("pane:folders", folderBox.Render(strings.Join(visibleFolders, "\n")))

	rightInnerWidth := m.width - folderPaneWidth - 2

	var rightPane string

	if m.draftVisible && m.draft != nil {
		draft := m.draft
		labelStyle := m.style(mutedColor).Width(10)

		lines := []string{
			" " + boldStyle.Render("Draft") + m.style(mutedColor).Render("  from ") + m.style(m.accountColor(draft.Account)).Bold(true).Render(draft.Account),
			"",
			" " + labelStyle.Render("To") + fit(draft.To, rightInnerWidth-12),
		}

		for _, field := range []struct{ label, value string }{{"Cc", draft.Cc}, {"Bcc", draft.Bcc}} {
			if field.value != "" {
				lines = append(lines, " "+labelStyle.Render(field.label)+fit(field.value, rightInnerWidth-12))
			}
		}

		lines = append(lines, " "+labelStyle.Render("Subject")+fit(draft.Subject, rightInnerWidth-12))

		for i, path := range draft.Attach {
			label := ""
			if i == 0 {
				label = "Attach"
			}

			status := ""
			if slices.Contains(m.missingAttachments, path) {
				status = m.style(redColor).Render("  not found")
			}

			lines = append(lines, " "+labelStyle.Render(label)+path+status)
		}

		lines = append(lines, " "+m.style(mutedColor).Render(strings.Repeat("─", rightInnerWidth-2)))

		statusLines := 0
		if m.draftError != "" {
			statusLines = 2
		}

		if m.sending || m.delivering {
			statusLines = 3
		}

		bodyHeight := max(0, innerHeight-len(lines)-statusLines)
		body := lipgloss.NewStyle().Width(rightInnerWidth - 2).Render(draft.Body)
		bodyLines := strings.Split(body, "\n")

		for _, line := range bodyLines[:min(len(bodyLines), bodyHeight)] {
			lines = append(lines, " "+line)
		}

		for len(lines) < innerHeight-statusLines {
			lines = append(lines, "")
		}

		switch {
		case m.delivering:
			lines = append(lines, "", " "+m.style(yellowColor).Render("Sending..."), "")

		case m.sending:
			barWidth := min(40, rightInnerWidth-4)
			filled := barWidth * (sendDelay - m.sendRemaining) / sendDelay
			bar := m.style(yellowColor).Render(strings.Repeat("━", filled)) + m.style(mutedColor).Render(strings.Repeat("━", barWidth-filled))
			lines = append(lines, "", " "+m.style(yellowColor).Bold(true).Render(fmt.Sprintf("Sending in %ds", m.sendRemaining))+"   "+m.button("u", "undo"), " "+bar)

		case m.draftError != "":
			lines = append(lines, "", " "+m.style(redColor).Render(ansi.Truncate(m.draftError, rightInnerWidth-2, "…")))
		}

		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.paint(m.accountColor(draft.Account))).Width(rightInnerWidth + 2).Height(innerHeight + 2)

		rightPane = box.Render(strings.Join(lines[:min(len(lines), innerHeight)], "\n"))
	} else {
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

		fromWidth := max(12, min(24, rightInnerWidth/4))
		dateWidth := 6

		subjectWidth := rightInnerWidth - 3 - fromWidth - 2 - 2 - dateWidth - 1
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

			lines = append(lines, "", lipgloss.PlaceHorizontal(rightInnerWidth, lipgloss.Center, m.style(mutedColor).Render(empty)))
		}

		focused := m.focus == listPane && !m.searchFocused
		now := time.Now()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)

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

			sent := message.Date.Local()
			sentDay := time.Date(sent.Year(), sent.Month(), sent.Day(), 0, 0, 0, 0, time.Local)
			date := sent.Format("Jan 06")

			switch {
			case sentDay.Equal(today):
				date = sent.Format("15:04")

			case !sentDay.Before(today.AddDate(0, 0, -6)):
				date = sent.Format("Mon")

			case sent.Year() == now.Year():
				date = sent.Format("2 Jan")
			}

			line += "  " + lipgloss.PlaceHorizontal(dateWidth, lipgloss.Right, m.style(mutedColor).Render(date))

			lines = append(lines, m.zones.Mark(fmt.Sprintf("message:%d", i), fit(line, rightInnerWidth)))
		}

		borderColor := mutedColor
		if focused {
			borderColor = accentColor
		}

		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.paint(borderColor)).Width(rightInnerWidth + 2).Height(innerHeight + 2)

		rightPane = m.zones.Mark("pane:list", box.Render(strings.Join(lines, "\n")))
	}

	middle := lipgloss.JoinHorizontal(lipgloss.Top, folders, rightPane)

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
		switch {
		case m.pendingUnqueue != "":
			question = "Returning this email to the draft discards the current draft."

		case m.pendingCompose:
			question = "Starting a new email discards the current draft."
		}

		lines := []string{boldStyle.Render(question), "", m.button("y", "discard") + "    " + m.button("n", "keep")}

		middle = overlay(middle, m.popupBox(lines, max(lipgloss.Width(question)+4, 34)))

	case emptyTrashPopup:
		row := m.folderRows[m.folderIndex]
		question := fmt.Sprintf("Delete the %d emails in %s's Trash for good?", len(m.messages), row.account)
		lines := []string{boldStyle.Render(question), "This can't be undone.", "", m.button("y", "delete for good") + "    " + m.button("n", "keep")}

		middle = overlay(middle, m.popupBox(lines, max(lipgloss.Width(question)+4, 34)))

	case logPopup:
		boxWidth := min(m.width-8, 110)

		title := boldStyle.Render("Sync log")

		if m.logFilter != "" {
			title += m.style(mutedColor).Render(" · ") + m.style(m.accountColor(m.logFilter)).Bold(true).Render(m.logFilter)
		}

		lines := []string{
			title + m.style(mutedColor).Render("  "+m.options.LogDirectory),
			m.style(mutedColor).Render("Viewer  ") + m.options.Viewer.URL(),
			"",
		}

		entries := m.logEntries()

		if len(entries) == 0 {
			lines = append(lines, m.style(mutedColor).Render("Nothing logged yet."))
		}

		accountWidth := 0
		for _, account := range m.accounts {
			accountWidth = max(accountWidth, lipgloss.Width(account.Name))
		}

		start := min(m.logOffset, len(entries))

		for _, entry := range entries[start:min(len(entries), start+m.logRows())] {
			text := entry.Text

			if entry.Account != "" {
				text = m.style(m.accountColor(entry.Account)).Render(text)
			}

			line := m.style(mutedColor).Render(entry.Time) + "  " + fit(m.style(m.accountColor(entry.Account)).Bold(true).Render(entry.Account), accountWidth) + "  " + text

			lines = append(lines, fit(line, boxWidth-4))
		}

		middle = overlay(middle, m.popupBox(lines, boxWidth))
	}

	footer := strings.Join(m.footer(), "\n")

	view.SetContent(m.zones.Scan(lipgloss.JoinVertical(lipgloss.Left, topBar, middle, footer)))

	return view
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

// keys lists a key and its aliases, such as "j/down". A click runs the first.
func (m *Model) button(keys, label string) string {
	aliases := []string{keys}

	if keys != "/" {
		aliases = strings.Split(keys, "/")
	}

	var shown []string

	for _, key := range aliases {
		shown = append(shown, cmp.Or(keyNames[key], key))
	}

	return m.zones.Mark("key:"+aliases[0], m.style(mutedColor).Bold(true).Render(strings.Join(shown, "/"))+" "+m.style(mutedColor).Render(label))
}

func (m *Model) footer() []string {
	quit := m.button("q/ctrl+c", "quit")
	scroll := []string{m.button("j/down", "down"), m.button("k/up", "up"), m.button("pgdown", "page down"), m.button("pgup", "page up"), m.button("home", "top"), m.button("end", "bottom")}

	var accounts []string

	for i, account := range m.accounts[:min(9, len(m.accounts))] {
		key := strconv.Itoa(i + 1)
		nameStyle := m.style(m.accountColor(account.Name)).Bold(m.popup == logPopup && account.Name == m.logFilter)

		accounts = append(accounts, m.zones.Mark("key:"+key, m.style(mutedColor).Bold(true).Render(key)+" "+nameStyle.Render(account.Name)))
	}

	var buttons []string

	switch {
	case m.popup == movePopup:
		buttons = []string{m.button("enter", "move"), m.button("j/down", "down"), m.button("k/up", "up"), m.button("esc/q", "cancel"), m.button("ctrl+c", "quit")}

	case m.popup == discardPopup:
		buttons = []string{m.button("y", "discard"), m.button("n/esc/q", "keep"), m.button("ctrl+c", "quit")}

	case m.popup == emptyTrashPopup:
		buttons = []string{m.button("y", "delete for good"), m.button("n/esc/q", "keep"), m.button("ctrl+c", "quit")}

	case m.popup == logPopup:
		buttons = slices.Concat(accounts, scroll, []string{m.button("esc/q/L", "close"), m.button("ctrl+c", "quit")})

	case m.sending:
		buttons = []string{m.button("u", "undo send"), m.button("ctrl+c", "quit")}

	case m.delivering:
		buttons = []string{m.button("ctrl+c", "quit")}

	case m.searchFocused:
		buttons = []string{m.button("enter/tab", "browse results"), m.button("down", "down"), m.button("up", "up"), m.button("esc", "clear search"), m.button("ctrl+c", "quit")}

	case m.draftVisible:
		buttons = []string{m.button("e", "edit")}

		if m.draftError == "" {
			buttons = append(buttons, m.button("s", "send"))
		}

		buttons = append(buttons, m.button("h", "hide"), m.button("x", "discard"))
		buttons = slices.Concat(buttons, accounts, []string{m.button("ctrl+r", "sync"), m.button("n", "new"), m.button("/", "search"), m.button("L", "log"), quit})

	default:
		selected, hasSelection := m.selected()

		if hasSelection {
			movable := selected.Folder != mailbox.Outbox

			if movable && selected.Folder != mailbox.Archive {
				buttons = append(buttons, m.button("A", "archive"))
			}

			if movable {
				buttons = append(buttons, m.button("M", "move"))
			}

			switch {
			case selected.Folder == mailbox.Spam:
				buttons = append(buttons, m.button("!", "not spam"))

			case movable:
				buttons = append(buttons, m.button("!", "spam"))
			}

			switch {
			case selected.Folder == mailbox.Outbox:
				buttons = append(buttons, m.button("D", "back to draft"))

			case selected.Folder != mailbox.Trash:
				buttons = append(buttons, m.button("D", "delete"))
			}

			buttons = append(buttons, m.button("r", "reply all"), m.button("R", "reply"), m.button("y", "yank sender"))
		}

		row := m.folderRows[m.folderIndex]

		if m.search.Value() == "" && row.account != "" && row.folder == mailbox.Trash && len(m.messages) > 0 {
			buttons = append(buttons, m.button("E", "empty trash"))
		}

		if m.focus == listPane {
			buttons = append(buttons, m.button("h/left/tab", "sidebar"))
		} else {
			buttons = append(buttons, m.button("l/right/enter/tab", "email list"))
		}

		if m.focus == folderPane || len(m.messages) > 1 {
			buttons = append(buttons, scroll...)
		}

		if m.search.Value() != "" {
			buttons = append(buttons, m.button("esc", "clear search"))
		}

		buttons = slices.Concat(buttons, accounts, []string{m.button("ctrl+r", "sync"), m.button("n", "new"), m.button("/", "search"), m.button("L", "log")})

		if m.draft != nil {
			buttons = append(buttons, m.button("w", "draft"))
		}

		buttons = append(buttons, quit)
	}

	// Lay the buttons out in the most columns that fit, so none is cut off.
	available := m.width - 2
	columns := len(buttons)

	var widths []int

	for {
		widths = make([]int, columns)

		for i, button := range buttons {
			widths[i%columns] = max(widths[i%columns], lipgloss.Width(button))
		}

		total := 2 * (columns - 1)

		for _, width := range widths {
			total += width
		}

		if total <= available || columns == 1 {
			break
		}

		columns--
	}

	textWidth := 0
	for _, width := range widths {
		textWidth += width
	}

	gaps := max(1, columns-1)
	base, extra := 2, 0

	spacing := available - textWidth

	if spacing > gaps*2 {
		base, extra = spacing/gaps, spacing%gaps
	}

	var lines []string

	for start := 0; start < len(buttons); start += columns {
		row := buttons[start:min(len(buttons), start+columns)]

		var bar strings.Builder

		for column, button := range row {
			bar.WriteString(button)

			if column < len(row)-1 {
				gap := base
				if column < extra {
					gap++
				}

				bar.WriteString(strings.Repeat(" ", widths[column]-lipgloss.Width(button)+gap))
			}
		}

		lines = append(lines, " "+ansi.Truncate(bar.String(), available, ""))
	}

	for len(lines) < 2 {
		lines = append(lines, "")
	}

	return lines
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

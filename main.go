package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/BurntSushi/toml"
	"github.com/mattn/go-shellwords"

	"github.com/siliconwitch/scry/mailbox"
	"github.com/siliconwitch/scry/synclog"
	"github.com/siliconwitch/scry/tui"
	"github.com/siliconwitch/scry/viewer"
)

const version = "0.2.0"

type config struct {
	Viewer      string          `toml:"viewer"`
	Links       string          `toml:"links"`
	Notify      string          `toml:"notify"`
	LogDays     int             `toml:"log-days"`
	HistoryDays int             `toml:"history-days"`
	Accounts    []accountConfig `toml:"account"`
}

type accountConfig struct {
	Name            string   `toml:"name"`
	DisplayName     string   `toml:"display-name"`
	Email           string   `toml:"email"`
	IMAP            string   `toml:"imap"`
	SMTP            string   `toml:"smtp"`
	Signature       string   `toml:"signature"`
	PasswordCommand string   `toml:"password-cmd"`
	Folders         []string `toml:"folders"`
	CombinedInbox   *bool    `toml:"combined-inbox"`
}

func main() {
	home, err := os.UserHomeDir()

	if err != nil {
		fmt.Fprintln(os.Stderr, "scry:", err)
		os.Exit(1)
	}

	configPath := os.Getenv("SCRY_CONFIG")

	if configPath == "" {
		configDirectory, err := os.UserConfigDir()

		if err != nil {
			fmt.Fprintln(os.Stderr, "scry: finding the config directory:", err)
			os.Exit(1)
		}

		configPath = filepath.Join(configDirectory, "scry", "config.toml")
	}

	settings := config{LogDays: 7, HistoryDays: 365}

	metadata, err := toml.DecodeFile(configPath, &settings)

	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "scry: there is no config at %s. Start from config.example.toml.\n", configPath)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "scry: reading %s: %v\n", configPath, err)
		os.Exit(1)
	}

	var problems []string

	for _, key := range metadata.Undecoded() {
		problems = append(problems, fmt.Sprintf("%s isn't a setting.", key))
	}

	if settings.Links != "" && !strings.Contains(settings.Links, "%s") {
		problems = append(problems, `links needs %s where the address goes, such as "firefox %s".`)
	}

	if settings.LogDays < 1 {
		problems = append(problems, "log-days must be at least 1.")
	}

	if settings.HistoryDays < 1 {
		problems = append(problems, "history-days must be at least 1.")
	}

	command := func(key, value string) []string {
		words, err := shellwords.Parse(value)

		if err != nil {
			problems = append(problems, fmt.Sprintf("%s isn't a valid command. Check its quotes.", key))
		}

		return words
	}

	viewerCommand := command("viewer", settings.Viewer)
	linkCommand := command("links", settings.Links)
	notifyCommand := command("notify", settings.Notify)

	if len(settings.Accounts) == 0 {
		problems = append(problems, "There are no [[account]] blocks.")
	}

	var accounts []mailbox.Account

	numberByName := map[string]int{}
	numberByEmail := map[string]int{}

	for i, account := range settings.Accounts {
		number := i + 1

		for _, field := range []struct{ key, value string }{{"name", account.Name}, {"email", account.Email}, {"imap", account.IMAP}, {"smtp", account.SMTP}, {"password-cmd", account.PasswordCommand}} {
			if strings.TrimSpace(field.value) == "" {
				problems = append(problems, fmt.Sprintf("Account %d has no %s.", number, field.key))
			}
		}

		for _, field := range []struct{ key, value, example string }{{"imap", account.IMAP, "imap.gmail.com:993"}, {"smtp", account.SMTP, "smtp.gmail.com:465"}} {
			_, _, err := net.SplitHostPort(field.value)

			if field.value != "" && err != nil {
				problems = append(problems, fmt.Sprintf("Account %d: %s needs a host and a port, such as %q.", number, field.key, field.example))
			}
		}

		existing, duplicate := numberByName[account.Name]

		if account.Name != "" && duplicate {
			problems = append(problems, fmt.Sprintf("Accounts %d and %d are both called %q. Each account needs its own name.", existing, number, account.Name))
		}

		numberByName[account.Name] = number

		email := strings.ToLower(account.Email)
		existing, duplicate = numberByEmail[email]

		if email != "" && duplicate {
			problems = append(problems, fmt.Sprintf("Accounts %d and %d both use %s. Each account needs its own email.", existing, number, account.Email))
		}

		numberByEmail[email] = number

		listed := map[string]bool{}

		for _, folder := range account.Folders {
			switch {
			case strings.TrimSpace(folder) == "":
				problems = append(problems, fmt.Sprintf("Account %d lists an empty folder name.", number))

			case folder == mailbox.Outbox:
				problems = append(problems, fmt.Sprintf("Account %d lists an %s folder, but scry keeps that name for unsent email.", number, mailbox.Outbox))

			case slices.Contains([]string{mailbox.Inbox, mailbox.Sent, mailbox.Archive, mailbox.Spam, mailbox.Trash}, folder):
				problems = append(problems, fmt.Sprintf("Account %d lists %s in folders, but every account already shows it.", number, folder))

			case listed[folder]:
				problems = append(problems, fmt.Sprintf("Account %d lists %s in folders twice.", number, folder))
			}

			listed[folder] = true
		}

		signature := account.Signature
		rest, inHome := strings.CutPrefix(signature, "~/")

		if inHome {
			signature = filepath.Join(home, rest)
		}

		accounts = append(accounts, mailbox.Account{Name: account.Name, DisplayName: account.DisplayName, Email: account.Email, IMAP: account.IMAP, SMTP: account.SMTP, Signature: signature, PasswordCommand: command(fmt.Sprintf("Account %d: password-cmd", number), account.PasswordCommand), Folders: account.Folders, HideFromCombinedInbox: account.CombinedInbox != nil && !*account.CombinedInbox})
	}

	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "scry: %s has problems, so scry can't start:\n", configPath)

		for _, problem := range problems {
			fmt.Fprintln(os.Stderr, "  "+problem)
		}

		os.Exit(1)
	}

	stateDirectory := os.Getenv("XDG_STATE_HOME")

	if stateDirectory == "" {
		stateDirectory = filepath.Join(home, ".local", "state")
	}

	logDirectory := filepath.Join(stateDirectory, "scry", "logs")

	log, err := synclog.Open(logDirectory, settings.LogDays)

	if err != nil {
		fmt.Fprintln(os.Stderr, "scry: opening the sync log:", err)
		os.Exit(1)
	}

	server, err := viewer.Start(linkCommand)

	if err != nil {
		fmt.Fprintln(os.Stderr, "scry: starting the viewer:", err)
		os.Exit(1)
	}

	defer server.Close()

	viewerFile := ""
	runtimeDirectory := os.Getenv("XDG_RUNTIME_DIR")

	if runtimeDirectory != "" {
		viewerFile = filepath.Join(runtimeDirectory, "scry", "viewer")

		err := os.MkdirAll(filepath.Dir(viewerFile), 0o700)

		if err != nil {
			fmt.Fprintln(os.Stderr, "scry: writing the viewer address:", err)
			os.Exit(1)
		}

		temporary, err := os.CreateTemp(filepath.Dir(viewerFile), "viewer-*")

		if err != nil {
			fmt.Fprintln(os.Stderr, "scry: writing the viewer address:", err)
			os.Exit(1)
		}

		_, err = temporary.WriteString(server.URL())
		closeErr := temporary.Close()

		if err == nil {
			err = closeErr
		}

		if err == nil {
			err = os.Rename(temporary.Name(), viewerFile)
		}

		if err != nil {
			os.Remove(temporary.Name())
			fmt.Fprintln(os.Stderr, "scry: writing the viewer address:", err)
			os.Exit(1)
		}
	}

	startNotice := ""

	var words []string

	for _, word := range viewerCommand {
		words = append(words, strings.ReplaceAll(word, "%s", server.URL()))
	}

	if len(words) == 0 {
		startNotice = "No viewer command set. Press L for the viewer's address."
	} else {
		browser := exec.Command(words[0], words[1:]...)

		err := browser.Start()

		if err != nil {
			startNotice = "Can't start the viewer: " + err.Error()
		} else {
			go browser.Wait()
		}
	}

	mail := mailbox.New(accounts, filepath.Join(stateDirectory, "scry", "mirror"), settings.HistoryDays)

	root := tui.New(tui.Options{
		Mail:         mail,
		Viewer:       server,
		Log:          log,
		LogDirectory: logDirectory,
		StartNotice:  startNotice,
		Notify:       notifyCommand,
	})

	program := tea.NewProgram(root)

	_, err = program.Run()

	if viewerFile != "" {
		os.Remove(viewerFile)
	}

	saveErr := mail.Save()

	if err != nil {
		fmt.Fprintln(os.Stderr, "scry:", err)
		os.Exit(1)
	}

	if saveErr != nil {
		fmt.Fprintln(os.Stderr, "scry: saving the mirror:", saveErr)
		os.Exit(1)
	}
}

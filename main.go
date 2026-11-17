package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/BurntSushi/toml"

	"github.com/siliconwitch/scry/mailbox"
	"github.com/siliconwitch/scry/synclog"
	"github.com/siliconwitch/scry/tui"
	"github.com/siliconwitch/scry/viewer"
)

const version = "0.1.0"

type config struct {
	Viewer      string          `toml:"viewer"`
	LogDays     int             `toml:"log-days"`
	HistoryDays int             `toml:"history-days"`
	Accounts    []accountConfig `toml:"account"`
}

type accountConfig struct {
	Name            string   `toml:"name"`
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

	_, err = toml.DecodeFile(configPath, &settings)

	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "scry: reading config:", err)
		os.Exit(1)
	}

	if len(settings.Accounts) == 0 {
		fmt.Fprintln(os.Stderr, "scry: no [[account]] blocks in", configPath)
		os.Exit(1)
	}

	var accounts []mailbox.Account

	accountByEmail := map[string]string{}

	for _, account := range settings.Accounts {
		email := strings.ToLower(account.Email)
		existing, duplicate := accountByEmail[email]

		if duplicate {
			fmt.Fprintf(os.Stderr, "scry: %s and %s both use %s. Each account needs its own email.\n", existing, account.Name, account.Email)
			os.Exit(1)
		}

		accountByEmail[email] = account.Name

		if slices.Contains(account.Folders, mailbox.Outbox) {
			fmt.Fprintf(os.Stderr, "scry: %s lists an %s folder, but scry keeps that name for unsent email. Remove it from folders.\n", account.Name, mailbox.Outbox)
			os.Exit(1)
		}

		signature := account.Signature
		rest, inHome := strings.CutPrefix(signature, "~/")

		if inHome {
			signature = filepath.Join(home, rest)
		}

		accounts = append(accounts, mailbox.Account{Name: account.Name, Email: account.Email, IMAP: account.IMAP, SMTP: account.SMTP, Signature: signature, PasswordCommand: account.PasswordCommand, Folders: account.Folders, HideFromCombinedInbox: account.CombinedInbox != nil && !*account.CombinedInbox})
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

	server, err := viewer.Start()

	if err != nil {
		fmt.Fprintln(os.Stderr, "scry: starting the viewer:", err)
		os.Exit(1)
	}

	defer server.Close()

	startNotice := ""

	words := strings.Fields(strings.ReplaceAll(settings.Viewer, "%s", server.URL()))

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
	})

	program := tea.NewProgram(root)

	_, err = program.Run()

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

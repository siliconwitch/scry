package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/BurntSushi/toml"

	"github.com/siliconwitch/scry/mailbox"
	"github.com/siliconwitch/scry/synclog"
	"github.com/siliconwitch/scry/tui"
	"github.com/siliconwitch/scry/viewer"
)

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
	PasswordCommand string   `toml:"password-cmd"`
	Folders         []string `toml:"folders"`
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

	for _, account := range settings.Accounts {
		accounts = append(accounts, mailbox.Account{Name: account.Name, Email: account.Email, IMAP: account.IMAP, PasswordCommand: account.PasswordCommand, Folders: account.Folders})
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

	if settings.Viewer == "" {
		startNotice = "No viewer command set. Press L for the viewer's address."
	} else {
		words := strings.Fields(strings.ReplaceAll(settings.Viewer, "%s", server.URL()))
		browser := exec.Command(words[0], words[1:]...)

		if err := browser.Start(); err != nil {
			startNotice = "Can't start the viewer: " + err.Error()
		} else {
			go browser.Wait()
		}
	}

	root := tui.New(tui.Options{
		Mail:         mailbox.New(accounts, filepath.Join(stateDirectory, "scry", "mirror"), settings.HistoryDays),
		Viewer:       server,
		Log:          log,
		LogDirectory: logDirectory,
		StartNotice:  startNotice,
	})

	program := tea.NewProgram(root)

	_, err = program.Run()

	if err != nil {
		fmt.Fprintln(os.Stderr, "scry:", err)
		os.Exit(1)
	}
}

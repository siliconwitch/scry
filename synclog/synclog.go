package synclog

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Time    string
	Account string
	Text    string
}

type Log struct {
	mutex     sync.Mutex
	directory string
	entries   []Entry
}

func Open(directory string, keepDays int) (*Log, error) {
	err := os.MkdirAll(directory, 0o700)

	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(directory)

	if err != nil {
		return nil, err
	}

	cutoff := time.Now().AddDate(0, 0, -keepDays)

	var names []string

	for _, entry := range entries {
		day, err := time.ParseInLocation("2006-01-02.log", entry.Name(), time.Local)

		if err != nil {
			continue
		}

		if !day.Before(cutoff) {
			names = append(names, entry.Name())
			continue
		}

		err = os.Remove(filepath.Join(directory, entry.Name()))

		if err != nil {
			return nil, err
		}
	}

	slices.Sort(names)

	var loaded []Entry

	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(directory, name))

		if err != nil {
			continue
		}

		for _, line := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
			fields := strings.SplitN(line, "\t", 3)

			if len(fields) == 3 {
				loaded = append(loaded, Entry{Time: fields[0], Account: fields[1], Text: fields[2]})
				continue
			}

			stamp, text, _ := strings.Cut(line, "  ")
			loaded = append(loaded, Entry{Time: stamp, Text: text})
		}
	}

	return &Log{directory: directory, entries: loaded}, nil
}

func (l *Log) Write(account, format string, arguments ...any) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	now := time.Now()
	stamp := now.Format("2006-01-02 15:04:05")

	var written []Entry
	var content strings.Builder

	for _, line := range strings.Split(fmt.Sprintf(format, arguments...), "\n") {
		written = append(written, Entry{Time: stamp, Account: account, Text: line})
		content.WriteString(stamp + "\t" + account + "\t" + line + "\n")
	}

	l.entries = append(l.entries, written...)

	path := filepath.Join(l.directory, now.Format("2006-01-02.log"))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)

	if err != nil {
		return
	}

	defer file.Close()

	file.WriteString(content.String())
}

func (l *Log) Entries() []Entry {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return slices.Clip(l.entries)
}

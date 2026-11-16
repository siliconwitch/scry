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

type Log struct {
	mutex     sync.Mutex
	directory string
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

	for _, entry := range entries {
		day, err := time.ParseInLocation("2006-01-02.log", entry.Name(), time.Local)

		if err != nil || !day.Before(cutoff) {
			continue
		}

		err = os.Remove(filepath.Join(directory, entry.Name()))

		if err != nil {
			return nil, err
		}
	}

	return &Log{directory: directory}, nil
}

func (l *Log) Write(format string, arguments ...any) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	now := time.Now()

	path := filepath.Join(l.directory, now.Format("2006-01-02.log"))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)

	if err != nil {
		return
	}

	defer file.Close()

	fmt.Fprintf(file, "%s  %s\n", now.Format("2006-01-02 15:04:05"), fmt.Sprintf(format, arguments...))
}

func (l *Log) Lines() []string {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	entries, err := os.ReadDir(l.directory)

	if err != nil {
		return []string{"Can't read the log directory: " + err.Error()}
	}

	var names []string

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			names = append(names, entry.Name())
		}
	}

	slices.Sort(names)

	var lines []string

	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(l.directory, name))

		if err != nil {
			continue
		}

		lines = append(lines, strings.Split(strings.TrimRight(string(content), "\n"), "\n")...)
	}

	return lines
}

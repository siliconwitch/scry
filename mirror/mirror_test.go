package mirror

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestMirror(t *testing.T) {
	cases := []struct {
		name         string
		openPassword string
		damage       func(directory string)
		read         string
		want         string
		wantErr      func(error) bool
	}{
		{name: "round trip", openPassword: "secret", read: "messages/a", want: "hello"},
		{name: "wrong password", openPassword: "other", wantErr: func(err error) bool { return errors.Is(err, ErrWrongPassword) }},
		{name: "missing file", openPassword: "secret", read: "messages/missing", wantErr: func(err error) bool { return errors.Is(err, fs.ErrNotExist) }},
		{
			name:         "tampered file",
			openPassword: "secret",
			read:         "messages/a",
			damage: func(directory string) {
				path := filepath.Join(directory, "messages/a")
				content, _ := os.ReadFile(path)
				content[len(content)-1] ^= 1
				os.WriteFile(path, content, 0o600)
			},
			wantErr: func(err error) bool { return err != nil },
		},
		{
			name:         "swapped file",
			openPassword: "secret",
			read:         "messages/b",
			damage: func(directory string) {
				os.Rename(filepath.Join(directory, "messages/a"), filepath.Join(directory, "messages/b"))
			},
			wantErr: func(err error) bool { return err != nil },
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()

			writer, err := Open(directory, []byte("secret"))

			if err != nil {
				t.Fatal(err)
			}

			err = writer.Write("messages/a", []byte("hello"))

			if err != nil {
				t.Fatal(err)
			}

			if test.damage != nil {
				test.damage(directory)
			}

			reader, err := Open(directory, []byte(test.openPassword))

			if err == nil {
				var data []byte
				data, err = reader.Read(test.read)

				if err == nil && string(data) != test.want {
					t.Fatalf("read %q, want %q", data, test.want)
				}
			}

			if test.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if test.wantErr != nil && !test.wantErr(err) {
				t.Fatalf("wrong error: %v", err)
			}
		})
	}
}

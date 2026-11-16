package mirror

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

var ErrWrongPassword = errors.New("the mirror was encrypted with a different password")

type keyFile struct {
	Salt     []byte `json:"salt"`
	Time     uint32 `json:"time"`
	Memory   uint32 `json:"memory"`
	Threads  uint8  `json:"threads"`
	Verifier []byte `json:"verifier"`
}

type Mirror struct {
	directory string
	aead      cipher.AEAD
}

func Open(directory string, password []byte) (*Mirror, error) {
	err := os.MkdirAll(directory, 0o700)

	if err != nil {
		return nil, err
	}

	keyPath := filepath.Join(directory, "key")

	content, err := os.ReadFile(keyPath)

	if errors.Is(err, fs.ErrNotExist) {
		key := keyFile{Salt: make([]byte, 16), Time: 3, Memory: 64 * 1024, Threads: 4}
		rand.Read(key.Salt)

		aead, err := chacha20poly1305.NewX(argon2.IDKey(password, key.Salt, key.Time, key.Memory, key.Threads, chacha20poly1305.KeySize))

		if err != nil {
			return nil, err
		}

		nonce := make([]byte, aead.NonceSize())
		rand.Read(nonce)
		key.Verifier = aead.Seal(nonce, nonce, []byte("scry"), []byte("key"))

		content, err := json.Marshal(key)

		if err != nil {
			return nil, err
		}

		err = writeAtomic(keyPath, content)

		if err != nil {
			return nil, err
		}

		return &Mirror{directory: directory, aead: aead}, nil
	}

	if err != nil {
		return nil, err
	}

	var key keyFile

	err = json.Unmarshal(content, &key)

	if err != nil {
		return nil, fmt.Errorf("reading the mirror key: %w", err)
	}

	aead, err := chacha20poly1305.NewX(argon2.IDKey(password, key.Salt, key.Time, key.Memory, key.Threads, chacha20poly1305.KeySize))

	if err != nil {
		return nil, err
	}

	if len(key.Verifier) < aead.NonceSize() {
		return nil, errors.New("the mirror key is damaged")
	}

	_, err = aead.Open(nil, key.Verifier[:aead.NonceSize()], key.Verifier[aead.NonceSize():], []byte("key"))

	if err != nil {
		return nil, ErrWrongPassword
	}

	return &Mirror{directory: directory, aead: aead}, nil
}

func Wipe(directory string) error {
	return os.RemoveAll(directory)
}

func (m *Mirror) Write(name string, data []byte) error {
	path := filepath.Join(m.directory, name)

	err := os.MkdirAll(filepath.Dir(path), 0o700)

	if err != nil {
		return err
	}

	nonce := make([]byte, m.aead.NonceSize(), m.aead.NonceSize()+len(data)+m.aead.Overhead())
	rand.Read(nonce)

	return writeAtomic(path, m.aead.Seal(nonce, nonce, data, []byte(name)))
}

func (m *Mirror) Read(name string) ([]byte, error) {
	content, err := os.ReadFile(filepath.Join(m.directory, name))

	if err != nil {
		return nil, err
	}

	if len(content) < m.aead.NonceSize() {
		return nil, fmt.Errorf("%s in the mirror is damaged", name)
	}

	data, err := m.aead.Open(nil, content[:m.aead.NonceSize()], content[m.aead.NonceSize():], []byte(name))

	if err != nil {
		return nil, fmt.Errorf("%s in the mirror is damaged", name)
	}

	return data, nil
}

func (m *Mirror) Remove(name string) error {
	err := os.Remove(filepath.Join(m.directory, name))

	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

func writeAtomic(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".write-*")

	if err != nil {
		return err
	}

	_, err = temporary.Write(data)

	if err == nil {
		err = temporary.Sync()
	}

	closeErr := temporary.Close()

	if err == nil {
		err = closeErr
	}

	if err != nil {
		os.Remove(temporary.Name())
		return err
	}

	return os.Rename(temporary.Name(), path)
}

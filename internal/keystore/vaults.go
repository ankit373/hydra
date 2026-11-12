// SPDX-License-Identifier: MIT

package keystore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/ankit373/hydra/internal/util"
)

// ring is the credential-store API go-keyring exposes, narrowed so a test can
// stand in for the real Keychain. Get returns ErrNotFound for a missing item.
type ring interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

// keychainVault keeps each key as its own credential-store item. The index
// beside it names what is stored, so a process with nothing stored, which is
// most `hyctl` runs, never starts the keychain at all.
type keychainVault struct {
	ring    ring
	service string
	index   string
}

func (k keychainVault) get(name string) (string, error) { return k.ring.Get(k.service, name) }

func (k keychainVault) set(name, value string) error {
	if err := k.ring.Set(k.service, name, value); err != nil {
		return err
	}
	return update(k.index, func(names *[]string) {
		if !slices.Contains(*names, name) {
			*names = append(*names, name)
			slices.Sort(*names)
		}
	})
}

func (k keychainVault) del(name string) error {
	if err := k.ring.Delete(k.service, name); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return update(k.index, func(names *[]string) {
		*names = slices.DeleteFunc(*names, func(n string) bool { return n == name })
	})
}

func (k keychainVault) names() ([]string, error) {
	var names []string
	return names, readJSON(k.index, &names)
}

// fileVault is the store where no credential store is available: a JSON map
// written 0600, the same protection ~/.aws/credentials and ~/.netrc rely on.
type fileVault struct{ path string }

func (f fileVault) get(name string) (string, error) {
	m := map[string]string{}
	if err := readJSON(f.path, &m); err != nil {
		return "", err
	}
	v, ok := m[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f fileVault) set(name, value string) error {
	return update(f.path, func(m *map[string]string) {
		if *m == nil {
			*m = map[string]string{}
		}
		(*m)[name] = value
	})
}

func (f fileVault) del(name string) error {
	return update(f.path, func(m *map[string]string) { delete(*m, name) })
}

func (f fileVault) names() ([]string, error) {
	m := map[string]string{}
	if err := readJSON(f.path, &m); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	return names, nil
}

// readJSON leaves v untouched when path does not exist: nothing stored yet.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// update is a locked read-modify-write, renamed into place so a crash leaves
// the old file whole. CreateTemp makes the file 0600 before a byte is written.
func update[T any](path string, edit func(*T)) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := util.Lock(util.LockPath(path))
	if err != nil {
		return err
	}
	defer lock.Unlock()

	var v T
	if err := readJSON(path, &v); err != nil {
		return err
	}
	edit(&v)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".keystore-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

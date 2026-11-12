// SPDX-License-Identifier: MIT

// Package keystore keeps provider API keys in the operating system's credential
// store, so someone who never opens a terminal can still give Hydra a key.
package keystore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/ankit373/hydra/internal/config"
)

// ErrNotFound is a name with no stored key.
var ErrNotFound = errors.New("no stored key")

// BackendEnv set to "file" forces the private-file store on a machine that has
// a keychain: a locked keychain over SSH, or a test that must not touch the real one.
const BackendEnv = "HYDRA_KEYSTORE"

// maxKeyLen is well above any real key and well below the ~3 KB the macOS
// Keychain accepts once go-keyring has base64-encoded the value.
const maxKeyLen = 1024

// validName is an environment variable name, which is what a stored key is
// filed under and later exported as.
var validName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// vault is one place keys can live. names never reads a secret, so listing
// what is stored costs no keychain access.
type vault interface {
	get(name string) (string, error)
	set(name, value string) error
	del(name string) error
	names() ([]string, error)
}

// open picks the store per call rather than once, so $HYDRA_HOME and
// $HYDRA_KEYSTORE are honoured the way every other path in config.Dir() is.
func open() vault {
	if platformRing == nil || os.Getenv(BackendEnv) == "file" {
		return fileVault{path: filepath.Join(config.Dir(), "api_keys.json")}
	}
	// Scoped by the Hydra directory, so a $HYDRA_HOME is as isolated here as
	// it is everywhere else (#442).
	return keychainVault{
		ring:    platformRing,
		service: "hydra:" + config.Dir(),
		index:   filepath.Join(config.Dir(), "api_keys_keychain.json"),
	}
}

// Backend names where keys are kept, in words a settings screen can show.
func Backend() string {
	if _, ok := open().(fileVault); ok {
		return "a file only your user account can read"
	}
	if runtime.GOOS == "windows" {
		return "Windows Credential Manager"
	}
	return "the macOS Keychain"
}

// Get returns the key stored under name, or ErrNotFound.
func Get(name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	return open().get(name)
}

// Set stores value under name, trimmed of the whitespace a paste carries.
func Set(name, value string) error {
	if err := checkName(name); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if err := checkValue(value); err != nil {
		return err
	}
	return open().set(name, value)
}

// Delete removes the key stored under name. Removing one that is not there
// succeeds, since the state asked for is the state that holds.
func Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	return open().del(name)
}

// Names lists what is stored, without reading any secret.
func Names() ([]string, error) { return open().names() }

// loaded holds the stored keys this process has read. They never enter
// os.Environ, so no subprocess Hydra starts can inherit one.
var (
	loadedMu sync.RWMutex
	loaded   = map[string]string{}
)

// Load makes a stored key visible to Lookup in this process.
func Load(name, value string) {
	loadedMu.Lock()
	defer loadedMu.Unlock()
	loaded[name] = value
}

// Unload withdraws a key Load made visible.
func Unload(name string) {
	loadedMu.Lock()
	defer loadedMu.Unlock()
	delete(loaded, name)
}

// Lookup is how a provider credential is read: the first of names exported
// in the environment, which always wins, else the first one loaded from the store.
func Lookup(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	loadedMu.RLock()
	defer loadedMu.RUnlock()
	for _, n := range names {
		if v := loaded[n]; v != "" {
			return v
		}
	}
	return ""
}

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%q is not an environment variable name", name)
	}
	return nil
}

// checkValue never quotes the value: an error message is the one place a
// rejected key would otherwise end up on screen or in a log.
func checkValue(v string) error {
	switch {
	case v == "":
		return errors.New("the key is empty")
	case len(v) > maxKeyLen:
		return fmt.Errorf("the key is longer than %d characters, which no provider issues", maxKeyLen)
	}
	for _, r := range v {
		if r <= ' ' || r == 0x7f {
			return errors.New("the key contains spaces or control characters; paste just the key")
		}
	}
	return nil
}

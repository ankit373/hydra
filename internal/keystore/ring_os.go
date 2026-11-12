// SPDX-License-Identifier: MIT

//go:build darwin || windows

package keystore

import (
	"errors"

	keyring "github.com/zalando/go-keyring"
)

// platformRing is the macOS Keychain or the Windows Credential Manager. On
// macOS go-keyring drives /usr/bin/security and hands it the secret on stdin,
// never in argv, so a key never shows in a process listing.
var platformRing ring = osRing{}

type osRing struct{}

func (osRing) Get(service, user string) (string, error) {
	v, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (osRing) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (osRing) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// SPDX-License-Identifier: MIT

package keystore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const secret = "sk-test-xxxxxxxxxxxxxxxxWXYZ"

// fakeRing is the Keychain stand-in: a map, plus a count of every call so a
// test can prove a path never reached the credential store at all.
type fakeRing struct {
	items map[string]string
	calls int
	fail  error
}

func (f *fakeRing) Get(service, user string) (string, error) {
	f.calls++
	v, ok := f.items[service+"/"+user]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fakeRing) Set(service, user, password string) error {
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	f.items[service+"/"+user] = password
	return nil
}

func (f *fakeRing) Delete(service, user string) error {
	f.calls++
	if _, ok := f.items[service+"/"+user]; !ok {
		return ErrNotFound
	}
	delete(f.items, service+"/"+user)
	return nil
}

func withHome(t *testing.T, backend string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HYDRA_HOME", home)
	t.Setenv(BackendEnv, backend)
	return home
}

func withFakeRing(t *testing.T) *fakeRing {
	t.Helper()
	f := &fakeRing{items: map[string]string{}}
	orig := platformRing
	platformRing = f
	t.Cleanup(func() { platformRing = orig })
	return f
}

func TestFile_RoundTripIsPrivate(t *testing.T) {
	home := withHome(t, "file")

	if err := Set("OPENAI_API_KEY", "  "+secret+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := Get("OPENAI_API_KEY")
	if err != nil || got != secret {
		t.Fatalf("Get = %q, %v; want the pasted key with its whitespace trimmed", got, err)
	}
	if names, _ := Names(); !slices.Equal(names, []string{"OPENAI_API_KEY"}) {
		t.Errorf("Names = %v", names)
	}
	if !strings.Contains(Backend(), "file") {
		t.Errorf("Backend = %q, want the file store named", Backend())
	}

	info, err := os.Stat(filepath.Join(home, "api_keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no mode bits to check; its keys go to Credential Manager.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
	}

	if err := Delete("OPENAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("OPENAI_API_KEY"); err != nil {
		t.Errorf("deleting an absent key = %v, want success", err)
	}
	if _, err := Get("OPENAI_API_KEY"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestKeychain_SecretGoesToTheRingAndOnlyTheNameToDisk(t *testing.T) {
	home := withHome(t, "")
	ring := withFakeRing(t)

	if err := Set("ANTHROPIC_API_KEY", secret); err != nil {
		t.Fatal(err)
	}
	if ring.items["hydra:"+home+"/ANTHROPIC_API_KEY"] != secret {
		t.Fatalf("the key is not in the credential store under this Hydra home: %v", ring.items)
	}
	index, err := os.ReadFile(filepath.Join(home, "api_keys_keychain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), secret) || !strings.Contains(string(index), "ANTHROPIC_API_KEY") {
		t.Errorf("index must name the key and never hold it:\n%s", index)
	}
	if _, err := os.Stat(filepath.Join(home, "api_keys.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the keychain path wrote the plaintext file too")
	}
	if strings.Contains(Backend(), "file") {
		t.Errorf("Backend = %q while a credential store is in use", Backend())
	}

	if got, err := Get("ANTHROPIC_API_KEY"); err != nil || got != secret {
		t.Errorf("Get = %q, %v", got, err)
	}
	if err := Delete("ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if names, _ := Names(); len(names) != 0 {
		t.Errorf("Names after Delete = %v", names)
	}
	if err := Delete("ANTHROPIC_API_KEY"); err != nil {
		t.Errorf("deleting an absent key = %v, want success", err)
	}
}

// Most hyctl runs store nothing; listing must not spawn the keychain for them.
func TestKeychain_ListingNeverReadsASecret(t *testing.T) {
	withHome(t, "")
	ring := withFakeRing(t)

	if names, err := Names(); err != nil || len(names) != 0 {
		t.Fatalf("Names = %v, %v", names, err)
	}
	if err := Set("GROQ_API_KEY", secret); err != nil {
		t.Fatal(err)
	}
	before := ring.calls
	if _, err := Names(); err != nil {
		t.Fatal(err)
	}
	if ring.calls != before {
		t.Error("Names called the credential store")
	}
}

// A key the store refused must not be listed as stored.
func TestKeychain_RingFailureLeavesNoIndexEntry(t *testing.T) {
	withHome(t, "")
	ring := withFakeRing(t)
	ring.fail = errors.New("keychain locked")

	if err := Set("XAI_API_KEY", secret); err == nil {
		t.Fatal("Set succeeded although the credential store refused it")
	}
	if names, _ := Names(); len(names) != 0 {
		t.Errorf("Names = %v after a refused Set", names)
	}
}

func TestSet_RefusesWhatCannotBeAKey(t *testing.T) {
	withHome(t, "file")
	for _, name := range []string{"", "openai_api_key", "1KEY", "A-B", "A B"} {
		if err := Set(name, secret); err == nil {
			t.Errorf("Set(%q) accepted a name that is not an environment variable", name)
		}
	}
	for label, v := range map[string]string{
		"empty":    "   ",
		"space":    "sk-abc def",
		"newline":  "sk-abc\ndef",
		"too long": strings.Repeat("k", maxKeyLen+1),
	} {
		err := Set("OPENAI_API_KEY", v)
		if err == nil {
			t.Errorf("%s: accepted", label)
			continue
		}
		if strings.TrimSpace(v) != "" && strings.Contains(err.Error(), strings.TrimSpace(v)) {
			t.Errorf("%s: the error quotes the rejected key: %v", label, err)
		}
	}
	if _, err := Get("bad name"); err == nil {
		t.Error("Get accepted an invalid name")
	}
	if err := Delete("bad name"); err == nil {
		t.Error("Delete accepted an invalid name")
	}
}

// A loaded key is visible to Lookup and never to os.Environ, and an export
// of any of the names wins over it.
func TestLookup_ExportWinsAndStoredKeysStayOutOfTheEnvironment(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	Load("GEMINI_API_KEY", secret)
	t.Cleanup(func() { Unload("GEMINI_API_KEY") })

	if got := Lookup("GEMINI_API_KEY", "GOOGLE_API_KEY"); got != secret {
		t.Fatalf("Lookup = %q, want the loaded key", got)
	}
	if os.Getenv("GEMINI_API_KEY") != "" {
		t.Error("a stored key entered the process environment")
	}
	t.Setenv("GOOGLE_API_KEY", "exported-by-the-user")
	if got := Lookup("GEMINI_API_KEY", "GOOGLE_API_KEY"); got != "exported-by-the-user" {
		t.Errorf("Lookup = %q; an export of the second name must still win", got)
	}
	Unload("GEMINI_API_KEY")
	t.Setenv("GOOGLE_API_KEY", "")
	if got := Lookup("GEMINI_API_KEY", "GOOGLE_API_KEY"); got != "" {
		t.Errorf("Lookup after Unload = %q", got)
	}
}

func TestReadJSON_CorruptStoreIsAnError(t *testing.T) {
	home := withHome(t, "file")
	if err := os.WriteFile(filepath.Join(home, "api_keys.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Names(); err == nil {
		t.Error("a corrupt store listed as empty")
	}
	if err := Set("OPENAI_API_KEY", secret); err == nil {
		t.Error("Set wrote over a corrupt store")
	}
}

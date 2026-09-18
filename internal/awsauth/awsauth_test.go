// SPDX-License-Identifier: MIT

package awsauth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// writeSharedFiles lays out a machine configured the usual way: the shared
// credentials and config files under the sandbox's home, nothing in the
// environment.
func writeSharedFiles(t *testing.T, s *testutil.Sandbox, credentials, config string) {
	t.Helper()
	dir := filepath.Join(s.Home, ".aws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"credentials": credentials, "config": config} {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCredentials_ReadTheSharedFilesWhenTheEnvironmentIsEmpty(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeSharedFiles(t, s, `
# a comment, and a blank line above it
[default]
aws_access_key_id = AKIDFROMFILE
aws_secret_access_key = filesecret
`, `
[default]
region = eu-central-1
output = json
`)

	id, secret, token := Credentials()
	if id != "AKIDFROMFILE" || secret != "filesecret" {
		t.Errorf("Credentials() = %q/%q, want the shared file's pair", id, secret)
	}
	if token != "" {
		t.Errorf("session token = %q, want none", token)
	}
	if got := Region(); got != "eu-central-1" {
		t.Errorf("Region() = %q, want the config file's region", got)
	}

	if !Configured() {
		t.Error("Configured() = false on a machine set up through ~/.aws, which is why no head was listed")
	}
}

func TestCredentials_ProfileSelectsTheSection(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeSharedFiles(t, s, `
[default]
aws_access_key_id = DEFAULTKEY
aws_secret_access_key = defaultsecret

[work]
aws_access_key_id = WORKKEY
aws_secret_access_key = worksecret
aws_session_token = worktoken
`, `
[default]
region = us-east-1

[profile work]
region = ap-southeast-2
`)

	if id, _, _ := Credentials(); id != "DEFAULTKEY" {
		t.Errorf("with no AWS_PROFILE, id = %q, want the default profile", id)
	}
	t.Setenv("AWS_PROFILE", "work")
	id, secret, token := Credentials()
	if id != "WORKKEY" || secret != "worksecret" || token != "worktoken" {
		t.Errorf("Credentials() = %q/%q/%q, want the work profile", id, secret, token)
	}
	// The config file spells a named profile "[profile work]" and the
	// credentials file spells it "[work]". Reading both the same way finds the
	// region under neither.
	if got := Region(); got != "ap-southeast-2" {
		t.Errorf("Region() = %q, want the named profile's region", got)
	}
}

func TestCredentials_EnvironmentWinsAndIsNeverHalfMixed(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeSharedFiles(t, s, `
[default]
aws_access_key_id = AKIDFROMFILE
aws_secret_access_key = filesecret
`, "")

	s.SetKey(t, "AWS_ACCESS_KEY_ID", "AKIDFROMENV")
	s.SetKey(t, "AWS_SECRET_ACCESS_KEY", "envsecret")
	if id, secret, _ := Credentials(); id != "AKIDFROMENV" || secret != "envsecret" {
		t.Errorf("Credentials() = %q/%q, want the environment to win", id, secret)
	}

	// Half a credential in the environment is not a credential. Pairing it
	// with the file's other half signs as neither identity, and the failure
	// reads as a signature error rather than as the misconfiguration it is.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	id, secret, _ := Credentials()
	if id == "AKIDFROMENV" {
		t.Errorf("id = %q paired with secret %q: two halves of different credentials", id, secret)
	}
	if id != "AKIDFROMFILE" || secret != "filesecret" {
		t.Errorf("Credentials() = %q/%q, want the file's complete pair", id, secret)
	}
}

// A credential that has to be fetched is not one that can be read.
func TestCredentials_FetchedCredentialsAreNotUsableStaticKeys(t *testing.T) {
	cases := map[string]string{
		"assumed role": `
[default]
role_arn = arn:aws:iam::123456789012:role/Deploy
source_profile = base
aws_access_key_id = SHOULDNOTBEUSED
aws_secret_access_key = neither
`,
		"sso session": `
[default]
sso_session = corp
sso_account_id = 123456789012
`,
		"credential process": `
[default]
credential_process = /usr/local/bin/fetch-creds
`,
	}
	for name, credentials := range cases {
		t.Run(name, func(t *testing.T) {
			s := testutil.NewSandbox(t)
			writeSharedFiles(t, s, credentials, "[default]\nregion = eu-west-1\n")

			id, secret, _ := Credentials()
			if id != "" || secret != "" {
				t.Errorf("Credentials() = %q/%q, want nothing: these need a token exchange", id, secret)
			}
			// The region is still configuration, and still readable.
			if got := Region(); got != "eu-west-1" {
				t.Errorf("Region() = %q, want the profile's region", got)
			}
		})
	}
}

func TestCredentials_HonourTheFileLocationOverrides(t *testing.T) {
	s := testutil.NewSandbox(t)
	elsewhere := filepath.Join(s.Home, "vault", "creds.ini")
	if err := os.MkdirAll(filepath.Dir(elsewhere), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elsewhere,
		[]byte("[default]\naws_access_key_id = MOVEDKEY\naws_secret_access_key = movedsecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", elsewhere)

	if id, _, _ := Credentials(); id != "MOVEDKEY" {
		t.Errorf("id = %q, want the file AWS_SHARED_CREDENTIALS_FILE names", id)
	}
}

func TestCredentials_NoFilesIsNotAnError(t *testing.T) {
	testutil.NewSandbox(t)
	if id, secret, _ := Credentials(); id != "" || secret != "" {
		t.Errorf("Credentials() = %q/%q on a machine with no AWS setup", id, secret)
	}
}

// SPDX-License-Identifier: MIT

package awsconf

import (
	"os"
	"path/filepath"
	"testing"
)

// awsHome points the resolver at a temp ~/.aws and clears every AWS variable,
// so a developer's real credentials cannot make a test pass or fail.
func awsHome(t *testing.T, credentials, config string) string {
	t.Helper()
	for _, k := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE",
		"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE",
	} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	// HOME *and* both file variables, always. Pointing only at written files
	// let the empty case fall back to the developer's real ~/.aws/config, and
	// TestResolve_NoFilesIsNotUsable duly read a live region off this machine.
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	write := func(name, body, envVar string) {
		p := filepath.Join(dir, name)
		if body != "" {
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Set even when nothing was written, so the absent case names a path
		// inside the sandbox rather than falling through to the real home.
		t.Setenv(envVar, p)
	}
	write("credentials", credentials, "AWS_SHARED_CREDENTIALS_FILE")
	write("config", config, "AWS_CONFIG_FILE")
	return dir
}

const sharedCreds = `# a comment
[default]
aws_access_key_id = AKIADEFAULT
aws_secret_access_key = secretdefault

[work]
aws_access_key_id = AKIAWORK
aws_secret_access_key = secretwork
aws_session_token = tokwork

[rolesonly]
role_arn = arn:aws:iam::111122223333:role/Reader
source_profile = default
`

const sharedConfig = `[default]
region = us-east-1

[profile work]
region = eu-west-1

[unprefixed]
region = ap-south-1
`

// The bug: a machine configured the way the AWS CLI expects showed no head.
func TestResolve_ReadsTheSharedFilesWhenTheEnvironmentIsEmpty(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)

	c := Resolve()
	if !c.Usable() {
		t.Fatalf("Resolve() = %+v, want the default profile's keys", c)
	}
	if c.AccessKeyID != "AKIADEFAULT" || c.SecretAccessKey != "secretdefault" {
		t.Errorf("keys = %q/%q, want the default profile's", c.AccessKeyID, c.SecretAccessKey)
	}
	if c.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1 from ~/.aws/config", c.Region)
	}
}

func TestResolve_AWSProfileSelectsTheProfile(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_PROFILE", "work")

	c := Resolve()
	if c.AccessKeyID != "AKIAWORK" || c.SessionToken != "tokwork" {
		t.Errorf("Resolve() = %+v, want the work profile including its session token", c)
	}
	// ~/.aws/config prefixes a non-default profile with "profile ".
	if c.Region != "eu-west-1" {
		t.Errorf("Region = %q, want eu-west-1 from [profile work]", c.Region)
	}
}

// A file written by hand often omits the "profile " prefix. Accepting both is
// the difference between a working region and a silently empty one.
func TestResolve_AcceptsAnUnprefixedConfigSection(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_PROFILE", "unprefixed")
	if got := Resolve().Region; got != "ap-south-1" {
		t.Errorf("Region = %q, want ap-south-1 from an unprefixed [unprefixed]", got)
	}
}

// Assuming a role needs a token exchange, not a file read. A profile carrying
// only a role reference must not read as a usable credential, or discovery
// advertises a head that cannot sign anything.
func TestResolve_ARoleOnlyProfileIsNotACredential(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_PROFILE", "rolesonly")

	if c := Resolve(); c.Usable() {
		t.Errorf("Resolve() = %+v is Usable, but the profile only names a role", c)
	}
}

// The environment is the documented first source and must win outright.
func TestResolve_EnvironmentBeatsTheFiles(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secretenv")
	t.Setenv("AWS_REGION", "sa-east-1")

	c := Resolve()
	if c.AccessKeyID != "AKIAENV" || c.SecretAccessKey != "secretenv" {
		t.Errorf("keys = %q/%q, want the environment's", c.AccessKeyID, c.SecretAccessKey)
	}
	if c.Region != "sa-east-1" {
		t.Errorf("Region = %q, want the environment's", c.Region)
	}
}

// Each field falls back on its own: a region in the environment with keys in a
// file is an ordinary setup and must work.
func TestResolve_FieldsFallBackIndependently(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_REGION", "ca-central-1")

	c := Resolve()
	if c.AccessKeyID != "AKIADEFAULT" {
		t.Errorf("AccessKeyID = %q, want the file's while the region came from the env", c.AccessKeyID)
	}
	if c.Region != "ca-central-1" {
		t.Errorf("Region = %q, want the environment's", c.Region)
	}
}

// A key set is taken whole. An id from the environment paired with a secret
// from a file signs nothing, and would be far harder to diagnose than an
// absent head.
func TestResolve_DoesNotMixAnEnvIDWithAFileSecret(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")

	c := Resolve()
	if c.AccessKeyID == "AKIAENV" && c.SecretAccessKey == "secretdefault" {
		t.Fatal("paired an environment id with a file secret; that signature can only fail")
	}
	if c.AccessKeyID != "AKIADEFAULT" || c.SecretAccessKey != "secretdefault" {
		t.Errorf("Resolve() = %+v, want the file's pair taken whole", c)
	}
}

// No files at all is the common case on a machine with no AWS setup, and must
// be silent rather than an error or a panic.
func TestResolve_NoFilesIsNotUsable(t *testing.T) {
	awsHome(t, "", "")
	if c := Resolve(); c.Usable() || c.Region != "" {
		t.Errorf("Resolve() = %+v with no environment and no files, want empty", c)
	}
}

// A section that is present but empty, and one that is absent, are different
// answers; conflating them would let a later lookup read another profile's keys.
func TestSection_AbsentIsNilAndEmptyIsNot(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "credentials")
	if err := os.WriteFile(p, []byte("[empty]\n\n[other]\nk = v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := section(p, "empty"); got == nil {
		t.Error("an empty but present section read as absent")
	}
	if got := section(p, "missing"); got != nil {
		t.Errorf("an absent section read as present: %v", got)
	}
	if got := section(p, "other"); got["k"] != "v" {
		t.Errorf("section did not stop at the preceding header: %v", got)
	}
}

// A credential the profile says must be fetched is not one that can be read:
// the static pair beside a role_arn is what obtains the role, so signing with
// it acts as the base principal instead of the role (#890).
func TestResolve_AFetchedCredentialIsNotTheStaticKeysBesideIt(t *testing.T) {
	cases := map[string]struct{ credentials, config string }{
		"assumed role in credentials": {
			credentials: "[default]\nrole_arn = arn:aws:iam::123456789012:role/Deploy\n" +
				"aws_access_key_id = AKIDBASE\naws_secret_access_key = basesecret\n",
			config: "[default]\nregion = eu-west-1\n",
		},
		"assumed role in config, stale keys in credentials": {
			credentials: "[default]\naws_access_key_id = AKIDBASE\naws_secret_access_key = basesecret\n",
			config:      "[default]\nregion = eu-west-1\nrole_arn = arn:aws:iam::123456789012:role/Deploy\nsource_profile = base\n",
		},
		"sso session with stale keys": {
			credentials: "[default]\naws_access_key_id = AKIDSTALE\naws_secret_access_key = stalesecret\n",
			config:      "[default]\nregion = eu-west-1\nsso_session = corp\nsso_account_id = 123456789012\n",
		},
		"credential process": {
			credentials: "[default]\naws_access_key_id = AKIDSTALE\naws_secret_access_key = stalesecret\n",
			config:      "[default]\nregion = eu-west-1\ncredential_process = /usr/local/bin/fetch-creds\n",
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			awsHome(t, files.credentials, files.config)

			got := Resolve()
			if got.Usable() {
				t.Errorf("Usable() = true with %q/%q: that pair obtains the identity, it is not the identity",
					got.AccessKeyID, got.SecretAccessKey)
			}
			// A region is configuration rather than a credential, and is still
			// readable from such a profile.
			if got.Region != "eu-west-1" {
				t.Errorf("Region = %q, want the profile's region", got.Region)
			}
		})
	}
}

// The environment is not a profile: keys given there are the identity, whatever
// a profile in the files happens to declare.
func TestResolve_EnvironmentKeysSurviveAProfileThatFetches(t *testing.T) {
	awsHome(t, "[default]\naws_access_key_id = AKIDSTALE\naws_secret_access_key = stalesecret\n",
		"[default]\nregion = eu-west-1\nrole_arn = arn:aws:iam::123456789012:role/Deploy\n")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFROMENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "envsecret")

	got := Resolve()
	if !got.Usable() || got.AccessKeyID != "AKIDFROMENV" {
		t.Errorf("Resolve() = %q/%q, want the environment's own credential", got.AccessKeyID, got.SecretAccessKey)
	}
}

// A plain static profile still resolves, or the guard would have taken the
// credential chain with it.
func TestResolve_AStaticProfileIsStillUsable(t *testing.T) {
	awsHome(t, "[default]\naws_access_key_id = AKIDSTATIC\naws_secret_access_key = staticsecret\n",
		"[default]\nregion = us-east-1\n")

	got := Resolve()
	if !got.Usable() || got.AccessKeyID != "AKIDSTATIC" || got.Region != "us-east-1" {
		t.Errorf("Resolve() = %+v, want the static profile", got)
	}
}

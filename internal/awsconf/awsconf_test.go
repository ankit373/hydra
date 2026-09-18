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

[roleplus]
aws_access_key_id = AKIABASE
aws_secret_access_key = secretbase
role_arn = arn:aws:iam::111122223333:role/Writer

[ssoconf]
aws_access_key_id = AKIASTALE
aws_secret_access_key = secretstale
`

const sharedConfig = `[default]
region = us-east-1

[profile work]
region = eu-west-1

[profile roleplus]
region = us-west-1

[profile ssoconf]
region = eu-north-1
sso_session = corp

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
//
// This one passed for the wrong reason before #890: there were no keys, so
// Usable() was false whether or not role_arn was noticed at all. The case below
// is the one that actually exercises the check.
func TestResolve_ARoleOnlyProfileIsNotACredential(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_PROFILE", "rolesonly")

	c := Resolve()
	if c.Usable() {
		t.Errorf("Resolve() = %+v is Usable, but the profile only names a role", c)
	}
	if c.Deferred != "role_arn" {
		t.Errorf("Deferred = %q, want role_arn: without it this passes merely because "+
			"the profile has no keys", c.Deferred)
	}
}

// Usable is tested directly, not only through Resolve. Resolve also *clears*
// the keys on a deferring profile, so a Usable that ignored Deferred still
// answered false through that path and the mutation went uncaught: two
// mechanisms reaching the same outcome is how a guard goes vacuous. Both are
// wanted (clearing stops a caller reading the keys at all; the check states the
// invariant), so both are pinned at their own level.
func TestCreds_UsableIsFalseWhateverKeysSitBesideADeferral(t *testing.T) {
	c := Creds{AccessKeyID: "AKIA", SecretAccessKey: "s", Deferred: "role_arn"}
	if c.Usable() {
		t.Error("Usable() = true with Deferred set: these keys obtain the identity, " +
			"they are not it, so signing with them acts as the base principal")
	}
	c.Deferred = ""
	if !c.Usable() {
		t.Error("Usable() = false for a plain key pair; the check is too strict")
	}
}

// The real bug. Static keys sitting beside role_arn are the credential used to
// *obtain* the identity, not the identity itself, so signing with them acts as
// the base principal rather than the role. Hydra performs no token exchange, so
// the honest answer is no credential, never the wrong one (#890).
func TestResolve_StaticKeysBesideARoleAreNotTheIdentity(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_PROFILE", "roleplus")

	c := Resolve()
	if c.Usable() {
		t.Errorf("Resolve() = %+v is Usable: Hydra would sign as the base principal "+
			"instead of the role the profile names", c)
	}
	if c.Deferred != "role_arn" {
		t.Errorf("Deferred = %q, want role_arn", c.Deferred)
	}
}

// role_arn and sso_session conventionally live in ~/.aws/config while the keys
// live in ~/.aws/credentials, so checking only the credentials file would miss
// the ordinary assume-role setup entirely.
func TestResolve_ADeferralDeclaredInConfigCounts(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_PROFILE", "ssoconf")

	c := Resolve()
	if c.Usable() {
		t.Errorf("Resolve() = %+v is Usable though [profile ssoconf] in ~/.aws/config "+
			"declares sso_session", c)
	}
	if c.AccessKeyID != "" {
		t.Errorf("AccessKeyID = %q, want it cleared: a key left on a struct that means "+
			"\"not usable\" is exactly the footgun this fixes", c.AccessKeyID)
	}
}

// A region is configuration, not a credential. A deferring profile is
// unroutable for want of an identity, and reporting a second missing thing
// would send someone to fix the wrong one.
func TestResolve_ADeferringProfileStillYieldsItsRegion(t *testing.T) {
	awsHome(t, sharedCreds, sharedConfig)
	t.Setenv("AWS_PROFILE", "roleplus")

	if got := Resolve().Region; got != "us-west-1" {
		t.Errorf("Region = %q, want us-west-1: the region is still readable", got)
	}
}

// Every directive that means "fetch this identity" is covered, not just the one
// that prompted the fix.
func TestResolve_EveryDeferralDirectiveIsRecognised(t *testing.T) {
	for _, key := range deferredKeys {
		t.Run(key, func(t *testing.T) {
			awsHome(t, "[default]\naws_access_key_id = AKIA\naws_secret_access_key = s\n"+
				key+" = something\n", "")
			c := Resolve()
			if c.Usable() {
				t.Errorf("%s did not defer: Resolve() = %+v", key, c)
			}
			if c.Deferred != key {
				t.Errorf("Deferred = %q, want %q", c.Deferred, key)
			}
		})
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

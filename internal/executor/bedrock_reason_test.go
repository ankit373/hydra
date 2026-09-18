// SPDX-License-Identifier: MIT

package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// SupportsHTTP folds credential, region and model into one bool, so the generic
// refusal named only the first of them. Someone with a key sitting in
// ~/.aws/credentials and no region was told they had no key, which sends them
// to look in the wrong place: a wrong reason is worse than a vague one (#890).

// awsFiles points HOME and both AWS file variables at a temp dir, so a
// developer's real ~/.aws cannot decide the outcome.
func awsFiles(t *testing.T, credentials, config string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	for name, body := range map[string]string{"credentials": credentials, "config": config} {
		p := filepath.Join(dir, name)
		if body != "" {
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if name == "credentials" {
			t.Setenv("AWS_SHARED_CREDENTIALS_FILE", p)
		} else {
			t.Setenv("AWS_CONFIG_FILE", p)
		}
	}
}

func TestBedrockUnroutable_NamesTheRegionWhenTheKeyIsPresent(t *testing.T) {
	testutil.NewSandbox(t)
	awsFiles(t, "[default]\naws_access_key_id = AKIA\naws_secret_access_key = s\n", "")

	got := Unroutable(bedrockHead())
	if !strings.Contains(got, "region") {
		t.Errorf("Unroutable = %q, want it to name the missing region", got)
	}
	if strings.Contains(got, "no API key") || strings.Contains(got, "credentials") {
		t.Errorf("Unroutable = %q, but the credentials are present: this sends someone "+
			"to look in the wrong place", got)
	}
}

func TestBedrockUnroutable_NamesCredentialsWhenThereAreNone(t *testing.T) {
	testutil.NewSandbox(t)
	awsFiles(t, "", "[default]\nregion = us-east-1\n")

	if got := Unroutable(bedrockHead()); !strings.Contains(got, "credentials") {
		t.Errorf("Unroutable = %q, want it to name the missing credentials", got)
	}
}

// A profile that defers is not "no credentials": the fix is to stop deferring
// or export static keys, not to go looking for a file that is already there.
func TestBedrockUnroutable_NamesTheDeferralRatherThanAMissingKey(t *testing.T) {
	testutil.NewSandbox(t)
	awsFiles(t,
		"[default]\naws_access_key_id = AKIA\naws_secret_access_key = s\n"+
			"role_arn = arn:aws:iam::1:role/R\n",
		"[default]\nregion = us-east-1\n")

	got := Unroutable(bedrockHead())
	if !strings.Contains(got, "role_arn") {
		t.Errorf("Unroutable = %q, want it to name role_arn as what defers the identity", got)
	}
}

// Whatever the state, the head must never read as routable while SupportsHTTP
// says no: an empty string here means "dispatch to it".
func TestBedrockUnroutable_NeverEmptyWhileUnsupported(t *testing.T) {
	testutil.NewSandbox(t)
	awsFiles(t, "", "")

	h := bedrockHead()
	if SupportsHTTP(h) {
		t.Fatal("SupportsHTTP is true with no credentials, no region and no model; " +
			"the sandbox did not isolate the AWS environment")
	}
	if got := Unroutable(h); got == "" {
		t.Error("Unroutable is empty while SupportsHTTP is false, so a head that cannot " +
			"run would be dispatched to")
	}
}

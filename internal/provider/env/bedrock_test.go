// SPDX-License-Identifier: MIT

package env

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/testutil"
)

// The symptom: `aws` works on the machine and Hydra lists no Bedrock head at
// all, because discovery only ever looked at the environment (#867).
func TestDiscover_BedrockFromTheSharedCredentialsFile(t *testing.T) {
	s := testutil.NewSandbox(t)
	dir := filepath.Join(s.Home, ".aws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials"),
		[]byte("[default]\naws_access_key_id = AKIDFROMFILE\naws_secret_access_key = filesecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasHead(heads, "env/bedrock") {
		t.Errorf("no env/bedrock head: %+v", heads)
	}
}

// An SSO profile names a credential that still has to be fetched, so listing a
// head for it would promise something no dispatch can deliver.
func TestDiscover_NoBedrockHeadForAProfileThatNeedsATokenExchange(t *testing.T) {
	s := testutil.NewSandbox(t)
	dir := filepath.Join(s.Home, ".aws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"),
		[]byte("[default]\nregion = eu-west-1\nsso_session = corp\nsso_account_id = 123456789012\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasHead(heads, "env/bedrock") {
		t.Errorf("listed a Bedrock head for an SSO profile: %+v", heads)
	}
}

func TestDiscover_NoBedrockHeadWithNoAWSSetupAtAll(t *testing.T) {
	testutil.NewSandbox(t)

	heads, err := (&Provider{}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasHead(heads, "env/bedrock") {
		t.Errorf("listed a Bedrock head on a machine with no AWS setup: %+v", heads)
	}
}

func hasHead(heads []provider.Head, id string) bool {
	for _, h := range heads {
		if h.ID == id {
			return true
		}
	}
	return false
}

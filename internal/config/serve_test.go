// SPDX-License-Identifier: MIT

package config_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// The E7 harness writes this block by hand, so its spelling is the contract.
func TestLoad_ReadsServeVerifiers(t *testing.T) {
	testutil.NewSandbox(t)
	body := `[[serve.verifiers]]
class  = "classification.banking77"
kind   = "match"
id     = "banking77"
labels = "/abs/banking77.labels"

[[serve.verifiers]]
class   = "extraction.amount"
kind    = "match"
id      = "amount"
pattern = '\d+'
`
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []config.ServeVerifier{
		{Class: "classification.banking77", Kind: "match", ID: "banking77", Labels: "/abs/banking77.labels"},
		{Class: "extraction.amount", Kind: "match", ID: "amount", Pattern: `\d+`},
	}
	if !reflect.DeepEqual(cfg.Serve.Verifiers, want) {
		t.Errorf("decoded %+v, want %+v", cfg.Serve.Verifiers, want)
	}
}

// SPDX-License-Identifier: MIT

//go:build !windows

package parallel

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// A validator runs files a model may have written, so it sees no credential.
func TestRunValidate_SeesNoCredential(t *testing.T) {
	probe := testutil.LeakProbe(t)
	if rc, err := runValidate(context.Background(), probe+" {file}", "x", nil); rc != 0 || err != nil {
		t.Fatalf("a credential reached the validator: rc=%d err=%v", rc, err)
	}
}

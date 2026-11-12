// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

// Within the TTL a probe is reused; a key saved in the app must not wait it out.
func TestForgetProbe_NextDispatcherProbesAgain(t *testing.T) {
	testutil.NewSandbox(t)

	first := cachedProbe(context.Background())
	if cachedProbe(context.Background()) != first {
		t.Fatal("the cache did not hold within its TTL, so this test proves nothing")
	}
	ForgetProbe()
	if cachedProbe(context.Background()) == first {
		t.Error("ForgetProbe left the cached probe in place")
	}
	t.Cleanup(ForgetProbe)
}

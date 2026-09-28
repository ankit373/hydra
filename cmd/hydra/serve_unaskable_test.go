// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/serve"
)

// A caller's own malformed payload must read as 400 about its request, not 502
// about a head that never answered and was not the reason.
func TestCallerError_AnUnaskableRequestIsTheCallersMistake(t *testing.T) {
	err := callerError(fmt.Errorf("dispatch: %w", executor.ErrUnaskable))
	if !errors.Is(err, serve.ErrBadRequest) {
		t.Errorf("the client would be told 502 for its own payload: %v", err)
	}
	// The cause has to survive, or the client is told it is wrong and not what.
	if !errors.Is(err, executor.ErrUnaskable) {
		t.Errorf("the original refusal was discarded: %v", err)
	}
}

// Everything else is still a head failure and still a 502.
func TestCallerError_AHeadFailureIsLeftAlone(t *testing.T) {
	boom := errors.New("all heads failed (tried 7)")
	if err := callerError(boom); errors.Is(err, serve.ErrBadRequest) {
		t.Errorf("a head failure was relabelled as the caller's mistake: %v", err)
	}
}

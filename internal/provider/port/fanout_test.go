// SPDX-License-Identifier: MIT

package port

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/capabilities"
	"github.com/ankit373/hydra/internal/provider"
)

// stubService stands in for a real one so the fan-out's cost and ordering can
// be asserted without depending on what is listening on the machine.
type stubService struct {
	dial  string
	delay time.Duration
	head  string
	err   error
}

func (s stubService) addr() string { return s.dial }

func (s stubService) probe(ctx context.Context, _ *capabilities.DB) ([]provider.Head, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return []provider.Head{{ID: s.head}}, nil
}

// listening returns an address that isOpen accepts, so these tests measure the
// fan-out rather than a dial timeout, which varies with the network.
func listening(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// Serially, each service's dial and probe was paid before the next one began,
// so the floor grew with the service list on every probe, status and dispatch
// (#750). Eight services that each take 150ms must cost about 150ms, not 1.2s.
func TestDiscover_ServicesAreProbedConcurrently(t *testing.T) {
	addr := listening(t)
	var services []portService
	for i := range 8 {
		services = append(services, stubService{dial: addr, delay: 150 * time.Millisecond, head: fmt.Sprintf("h%d", i)})
	}

	start := time.Now()
	heads := discover(context.Background(), caps(t), services)
	elapsed := time.Since(start)

	if len(heads) != 8 {
		t.Fatalf("got %d heads, want one per service: %+v", len(heads), heads)
	}
	// Halfway between one probe and two, so neither a slow machine nor a fast
	// serial loop can decide the result.
	if elapsed > 700*time.Millisecond {
		t.Errorf("8 services of 150ms took %v; they are still being probed one at a time", elapsed)
	}
}

// Head order decides display order and rank tie-breaks, so it must be the
// service order however the goroutines happen to interleave. The delays are
// descending, so an append-as-they-finish fan-out returns exactly backwards.
func TestDiscover_KeepsServiceOrderRegardlessOfCompletion(t *testing.T) {
	addr := listening(t)
	services := []portService{
		stubService{dial: addr, delay: 120 * time.Millisecond, head: "slowest"},
		stubService{dial: addr, delay: 60 * time.Millisecond, head: "middle"},
		stubService{dial: addr, head: "fastest"},
	}

	heads := discover(context.Background(), caps(t), services)

	want := []string{"slowest", "middle", "fastest"}
	if len(heads) != len(want) {
		t.Fatalf("got %d heads, want %d: %+v", len(heads), len(want), heads)
	}
	for i, id := range want {
		if heads[i].ID != id {
			t.Errorf("heads[%d] = %q, want %q (order is completion order, not service order)", i, heads[i].ID, id)
		}
	}
}

// One service failing must cost only that service's heads. Concurrently this
// is the case that a shared error slot or an early return would break.
func TestDiscover_SkipsUnprobeableAndClosedServices(t *testing.T) {
	addr := listening(t)
	services := []portService{
		stubService{dial: addr, head: "good"},
		stubService{dial: addr, head: "broken", err: errors.New("up but unprobeable")},
		stubService{dial: "127.0.0.1:1", head: "nothing-listening"},
		stubService{dial: "not-an-address", head: "malformed"},
		stubService{dial: addr, head: "also-good"},
	}

	heads := discover(context.Background(), caps(t), services)

	if len(heads) != 2 {
		t.Fatalf("got %d heads, want the two probeable ones: %+v", len(heads), heads)
	}
	if heads[0].ID != "good" || heads[1].ID != "also-good" {
		t.Errorf("got %q and %q, want good and also-good", heads[0].ID, heads[1].ID)
	}
}

func TestDiscover_NoServicesIsNoHeads(t *testing.T) {
	if got := discover(context.Background(), caps(t), nil); len(got) != 0 {
		t.Errorf("got %d heads from no services", len(got))
	}
}

// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// G7. A served temperature has no echo header, so the provider body is the
// only place it can be proven to have arrived (E7 runs every arm at 0).
func TestDispatch_TemperatureReachesTheWire(t *testing.T) {
	zero, warm := 0.0, 0.7
	for _, sent := range []*float64{nil, &zero, &warm} {
		var got map[string]json.RawMessage
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		}))
		d := streamDispatcher(t, provider.Head{
			ID: "wire", Name: "wire", Provider: "openai", Source: "env",
			Endpoint: srv.URL, AuthReady: true, CapScore: 90,
		})
		_, err := d.Dispatch(context.Background(), "hi", Options{Temperature: sent, RunID: "run-temp", TaskID: "task-temp"})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}

		raw, present := got["temperature"]
		var received *float64
		if present {
			var v float64
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			received = &v
		}
		if (received == nil) != (sent == nil) || (sent != nil && *received != *sent) {
			t.Errorf("head received temperature %v, client sent %v", showTemp(received), showTemp(sent))
		}
	}
}

func showTemp(f *float64) string {
	if f == nil {
		return "none"
	}
	return fmt.Sprint(*f)
}

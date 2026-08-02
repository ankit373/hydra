// SPDX-License-Identifier: MIT

// Package api is the desktop app's Go backend. It shapes Hydra's existing
// packages into DTOs the frontend can render directly.
//
// Two rules keep this package honest:
//
// It imports nothing from Wails. Every method is an ordinary Go function over
// ordinary structs, so the whole backend is unit-testable with no webview in
// the loop — `desktop/main.go` is the only file that knows Wails exists.
//
// It never recomputes a number Hydra already computes. Dashboard totals come
// from cost.Summary, cost.ByModel, and cost.GroupBy — the same calls `hyctl
// cost` and `hyctl stats` make — so the GUI and the CLI cannot drift into
// disagreeing about the same file. A test asserts exactly that.
package api

import (
	"context"

	"github.com/ankit373/hydra/internal/build"
)

// API is the type bound into the Wails runtime. It holds no mutable state; each
// call reads the logs fresh, which is what makes every method safe to call
// concurrently from the frontend.
type API struct {
	ctx context.Context
}

// New returns an API ready to bind.
func New() *API { return &API{} }

// Startup is Wails' OnStartup hook. The context it hands over is what the
// runtime needs for event emission later (Fleet, Session); holding it here
// keeps main.go free of anything but wiring.
func (a *API) Startup(ctx context.Context) { a.ctx = ctx }

// Version identifies the build, so the window can say what it is running.
type Version struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// GetVersion returns the ldflags-stamped build identity.
func (a *API) GetVersion() Version {
	return Version{Version: build.Version, Commit: build.Commit, Date: build.Date}
}

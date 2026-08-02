// SPDX-License-Identifier: MIT

//go:build desktop

// Command hydradesk is Hydra's desktop app.
//
// It lives in this module rather than its own repo because it binds directly to
// internal/cost, internal/trust, and internal/budget — Go's internal rule means
// only code inside this module tree can import them, so a separate repo would
// force those packages public first.
//
// This file is the only one that knows Wails exists. Everything it binds lives
// in desktop/api, which imports no UI framework and is unit-tested without a
// webview.
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/ankit373/hydra/desktop/api"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	a := api.New()

	err := wails.Run(&options.App{
		Title:  "Hydra",
		Width:  1280,
		Height: 860,
		// Below this the four-column dashboard grid stops being readable.
		MinWidth:  980,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// --hy-bg from brand/tokens.css. Set here too so the native window does
		// not flash white before the first paint.
		BackgroundColour: &options.RGBA{R: 0x06, G: 0x07, B: 0x0F, A: 1},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
		},
		OnStartup: a.Startup,
		Bind:      []any{a},
	})
	if err != nil {
		log.Fatalf("hydradesk: %v", err)
	}
}

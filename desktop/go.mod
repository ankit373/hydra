// The desktop app is a separate module so Wails' dependency tree — 25 extra
// requires, doubling go.sum — stays out of hyctl's supply chain. hyctl ships
// via brew/npm/pip/curl and never links a webview; carrying Wails in the root
// module would put its CVEs in the CLI's scan surface for nothing.
//
// The replace lets this module import github.com/ankit373/hydra/internal/... —
// Go's internal rule is scoped to the directory tree rooted at the parent of
// internal/, and desktop/ sits inside that tree.
module github.com/ankit373/hydra/desktop

go 1.25.0

// Pinned explicitly rather than left to resolve from the `go` line above.
// actions/setup-go reads this file (go-version-file: desktop/go.mod) to decide
// what to install, and without a toolchain line it installed exactly go1.25.0
// — confirmed from the real shipped v1.2.0-rc.2 binary's own `go version -m`
// output, not assumed. govulncheck found 21 reachable stdlib CVEs; go1.26.4
// (the root module's own pin) closed 20 of them, one — GO-2026-5856, an ECH
// privacy leak in crypto/tls — needing 1.26.5. Went one further than matching
// root rather than leave a known-fixed CVE open for no reason.
toolchain go1.26.5

require (
	github.com/ankit373/hydra v0.0.0
	github.com/wailsapp/wails/v2 v2.13.0
)

require (
	git.sr.ht/~jackmordaunt/go-toast/v2 v2.0.3 // indirect
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/bep/debounce v1.2.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jchv/go-winloader v0.0.0-20210711035445-715c2860da7e // indirect
	github.com/labstack/echo/v4 v4.15.3 // indirect
	github.com/labstack/gommon v0.5.0 // indirect
	github.com/leaanthony/go-ansi-parser v1.6.1 // indirect
	github.com/leaanthony/gosod v1.0.4 // indirect
	github.com/leaanthony/slicer v1.6.0 // indirect
	github.com/leaanthony/u v1.1.1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/samber/lo v1.49.1 // indirect
	github.com/tkrajina/go-reflector v0.5.8 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	github.com/wailsapp/go-webview2 v1.0.22 // indirect
	github.com/wailsapp/mimetype v1.4.1 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/ankit373/hydra => ../

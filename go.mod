module schroedinger-sync-go

go 1.26

// Pinned via govulncheck (2026-07-02): go1.26.1 carries 7 stdlib CVEs reachable from
// this codebase (net/textproto, crypto/x509 x4, net, crypto/tls — see SECURITY.md).
// Re-pinned 2026-07-11: go1.26.4 crypto/tls is reachable-affected by GO-2026-5856
// (Encrypted Client Hello privacy leak), fixed in go1.26.5.
// All are fixed upstream with no source change needed; GOTOOLCHAIN=auto downloads this
// automatically on build/test.
toolchain go1.26.5

// Run `go mod tidy` to resolve these from the imports:
//   github.com/billgraziano/dpapi   (Windows DPAPI)
//   github.com/chromedp/chromedp    (drives a real Chrome via CDP — clears Cloudflare's
//                                    JS challenge; the tls-client impersonation path this
//                                    project started with (v1) was fully replaced by CDP
//                                    in June 2026 and removed in the v2 hardening pass)
//   modernc.org/sqlite               (pure-Go SQLite, no cgo)

require (
	github.com/billgraziano/dpapi v0.5.0
	github.com/chromedp/cdproto v0.0.0-20260427013145-5737772c319b
	github.com/chromedp/chromedp v0.15.1
	github.com/gogpu/systray v0.1.1
	golang.org/x/sys v0.46.0
	modernc.org/sqlite v1.52.0
)

require (
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-json-experiment/json v0.0.0-20260214004413-d219187c3433 // indirect
	github.com/go-webgpu/goffi v0.5.5 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

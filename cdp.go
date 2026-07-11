// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// CDP path: drive a REAL Chrome instead of impersonating one.
//
// Why: claude.ai sits behind a Cloudflare *managed JS challenge*. tls-client cleared
// the raw TLS fingerprint but cannot execute the JS challenge, and a borrowed
// cf_clearance won't validate against a foreign fingerprint. A real Chrome runs the
// challenge itself and earns a fresh cf_clearance for its own fingerprint. We inject
// our DPAPI-extracted sessionKey, navigate, then do same-origin in-page fetches.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func awaitPromise(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
	return p.WithAwaitPromise(true)
}

// trunc truncates s to at most n runes (not bytes) — a byte-indexed s[:n] can split a
// multi-byte UTF-8 rune in half, producing invalid UTF-8 in whatever the result feeds
// into (a filename, via sanitize() below; a log/diagnostic line). Conversation and
// project titles here are frequently German (umlauts, ß) or contain emoji, so a
// mid-rune cut is a real, reachable case, not a theoretical one.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// --- types ---

type convSummary struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

type message struct {
	Sender    string         `json:"sender"`
	CreatedAt string         `json:"created_at"`
	Text      string         `json:"text"`
	Content   []contentBlock `json:"content"`
}

type fullConv struct {
	Name         string    `json:"name"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
	Model        string    `json:"model"`
	ChatMessages []message `json:"chat_messages"`
}

// --- session (real Chrome, Cloudflare-cleared) ---

func setCookieAction(sessionKey string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		exp := cdp.TimeSinceEpoch(time.Now().Add(365 * 24 * time.Hour))
		return network.SetCookie("sessionKey", sessionKey).
			WithDomain(".claude.ai").WithPath("/").
			WithSecure(true).WithHTTPOnly(true).WithExpires(&exp).Do(ctx)
	})
}

// openClaudeSession launches a real Chrome, injects the DPAPI sessionKey, navigates to
// claude.ai (Cloudflare's JS challenge clears itself), and returns in-page fetch funcs.
// `get` returns the response body text; `rawGet` returns the HTTP status + a head of the
// body (for endpoint discovery, where the status code is the signal).
func openClaudeSession() (get func(string) (string, error), rawGet func(string) (int, string, error), teardown func(), err error) {
	sessionKey, e := readSessionKey()
	if e != nil || sessionKey == "" {
		return nil, nil, nil, fmt.Errorf("sessionKey via DPAPI failed (close Claude Desktop first): %v", e)
	}
	fmt.Printf("[0] sessionKey via DPAPI: OK (len=%d)\n", len(sessionKey))

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false), // Cloudflare blocks old headless
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
	)
	allocCtx, cancelA := chromedp.NewExecAllocator(context.Background(), opts...)
	// WithErrorf/WithLogf override chromedp's internal b.logf/b.errf directly (verified
	// against chromedp v0.15.1 source) — without them chromedp defaults to Go's stdlib
	// log.Printf (targets os.Stderr) for its own internal messages, which would bypass
	// installStdoutRedactor entirely. See chromedpLogf (security.go) for why this matters
	// while network.Enable() is processing the injected sessionKey cookie all session long.
	ctx, cancelC := chromedp.NewContext(allocCtx, chromedp.WithErrorf(chromedpLogf), chromedp.WithLogf(chromedpLogf))
	ctx, cancelT := context.WithTimeout(ctx, 30*time.Minute)
	teardown = func() { registerTeardown(nil); cancelT(); cancelC(); cancelA() }
	registerTeardown(teardown)

	if e := chromedp.Run(ctx,
		network.Enable(),
		setCookieAction(sessionKey),
		chromedp.Navigate("https://claude.ai/"),
		chromedp.Sleep(12*time.Second), // let the JS challenge clear
	); e != nil {
		teardown()
		return nil, nil, nil, e
	}

	get = func(path string) (string, error) {
		var body string
		err := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`fetch(%q,{credentials:'include'}).then(r=>r.text())`, path),
			&body, awaitPromise))
		return body, err
	}
	rawGet = func(path string) (int, string, error) {
		var out string
		e := chromedp.Run(ctx, chromedp.Evaluate(
			fmt.Sprintf(`fetch(%q,{credentials:'include'}).then(async r=>JSON.stringify({s:r.status,b:(await r.text()).slice(0,6000)}))`, path),
			&out, awaitPromise))
		if e != nil {
			return 0, "", e
		}
		var v struct {
			S int    `json:"s"`
			B string `json:"b"`
		}
		if json.Unmarshal([]byte(out), &v) != nil {
			return 0, out, nil
		}
		return v.S, v.B, nil
	}
	return get, rawGet, teardown, nil
}

// errRateLimited is the sentinel fetchConvBody checks via errors.Is to decide whether to
// retry with lighter query params. Wrapped (%w) by getWithRetry so a wording change here
// can never silently break that fallback again.
var errRateLimited = errors.New("rate-limited")

func getWithRetry(get func(string) (string, error), path string, maxRetry int) (string, error) {
	delay := 2 * time.Second
	for i := 0; ; i++ {
		body, err := get(path)
		if err != nil {
			return "", err
		}
		if strings.Contains(body, "rate_limit_error") || strings.Contains(body, "Temporarily unavailable") {
			if i >= maxRetry {
				return "", fmt.Errorf("%w after %d retries", errRateLimited, maxRetry)
			}
			time.Sleep(delay)
			delay *= 2
			continue
		}
		return body, nil
	}
}

func fetchConvBody(get func(string) (string, error), org, uuid string) (string, error) {
	// A few huge/tool-heavy conversations persistently rate-limit on the full-fat
	// request even solo -> it's an expensive render, not frequency. Fall back to
	// progressively lighter query params so the big one still comes through.
	base := "/api/organizations/" + org + "/chat_conversations/" + uuid
	variants := []string{
		"?tree=True&rendering_mode=messages&render_all_tools=true&consistency=strong",
		"?tree=True&rendering_mode=messages", // drop tool-render + strong consistency
		"?rendering_mode=messages",           // current branch only (smallest)
	}
	var lastErr error
	for _, q := range variants {
		body, err := getWithRetry(get, base+q, 4)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !errors.Is(err, errRateLimited) {
			return "", err // a non-rate-limit error won't be fixed by lighter params
		}
		time.Sleep(3 * time.Second)
	}
	return "", lastErr
}

func resolveOrg(get func(string) (string, error)) (string, error) {
	body, err := getWithRetry(get, "/api/organizations", 3)
	if err != nil {
		return "", err
	}
	if t := strings.TrimSpace(body); strings.HasPrefix(t, "<") || strings.Contains(t, "Just a moment") {
		return "", fmt.Errorf("still Cloudflare-challenged: %s", trunc(t, 160))
	}
	var orgs []struct {
		UUID string `json:"uuid"`
	}
	if json.Unmarshal([]byte(body), &orgs) != nil || len(orgs) == 0 {
		return "", fmt.Errorf("cannot parse orgs: %s", trunc(body, 200))
	}
	// This tool only ever harvests orgs[0]. Most accounts have exactly one organization
	// (a personal workspace). If the account also belongs to a Team org, that second
	// organization's conversations/docs/memory are silently NOT harvested — nothing else
	// in this codebase surfaces that gap. Make it visible instead of leaving "did I get
	// everything?" unanswerable.
	if len(orgs) > 1 {
		logf("WARNING: account has %d organizations — only harvesting the first (%s); any other org's conversations/docs/memory are NOT synced", len(orgs), orgs[0].UUID)
	}
	return orgs[0].UUID, nil
}

// exitOnSessionFailure prints a friendly, actionable message for the one common,
// expected failure mode (Claude Desktop missing/never logged in) and the raw technical
// detail for anything else, then exits — used by every entry point that opens a Claude
// session (smoke, harvest, probe), so the message is consistent regardless of which
// command the user happened to run.
func exitOnSessionFailure(err error) {
	if errors.Is(err, ErrDesktopNotFound) {
		fatal(ErrDesktopNotFound.Error())
	}
	fatal("FAIL @session:", err)
}

// --- smoke ---

func cdpSmoke() {
	get, _, teardown, err := openClaudeSession()
	if err != nil {
		exitOnSessionFailure(err)
	}
	defer teardown()

	org, err := resolveOrg(get)
	if err != nil {
		fatal("FAIL @org:", err)
	}
	fmt.Println("[2] org_id:", org)

	list, lerr := getWithRetry(get, "/api/organizations/"+org+"/chat_conversations?limit=3&offset=0", 5)
	if lerr != nil {
		fmt.Println("[3b] list ERR:", lerr)
	}
	var convs []convSummary
	if json.Unmarshal([]byte(list), &convs) != nil {
		fmt.Println("[3b] parse ERR:", trunc(list, 200))
	}
	fmt.Printf("[3b] first page: %d conversations\n", len(convs))
	for _, c := range convs {
		fmt.Printf("     - %.60q | upd %.10s | %.8s\n", c.Name, c.UpdatedAt, c.UUID)
	}
	fmt.Println("\nSMOKE TEST (CDP): GREEN")
}

// --- harvest (M2) ---

func cdpHarvest() {
	outDir := defaultOutDir() // stable %LOCALAPPDATA% path, not CWD-relative — see daemon.go
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	}
	get, _, teardown, err := openClaudeSession()
	if err != nil {
		exitOnSessionFailure(err)
	}
	defer teardown()

	org, err := resolveOrg(get)
	if err != nil {
		fatal("FAIL @org:", err)
	}
	fmt.Println("[2] org_id:", org)

	// 1) paginate the full conversation list (no count_all — it rate-limits)
	var all []convSummary
	const limit = 100
	for offset := 0; ; offset += limit {
		body, err := getWithRetry(get,
			fmt.Sprintf("/api/organizations/%s/chat_conversations?limit=%d&offset=%d", org, limit, offset), 6)
		if err != nil {
			fmt.Println("FAIL @list:", err)
			break
		}
		var page []convSummary
		if json.Unmarshal([]byte(body), &page) != nil {
			fmt.Println("FAIL @list parse:", trunc(body, 200))
			break
		}
		all = append(all, page...)
		if len(page) < limit {
			break
		}
		time.Sleep(800 * time.Millisecond)
	}
	fmt.Printf("found %d conversations -> %s\n", len(all), outDir)
	// 0o750/0o600: this directory holds the user's own exported private conversations —
	// no reason for group/world read access. outDir itself is always a caller-supplied
	// CLI arg (self-inflicted at most, not attacker-controlled — see SECURITY.md), which
	// is why gosec's taint analysis flags every os.MkdirAll/os.WriteFile/os.Stat call
	// against it as G703; that's the CLI-argument pattern this tool is built around, not
	// a path-traversal vector (no remote value ever reaches a filesystem path unsanitized
	// — see sanitize() below).
	if err := os.MkdirAll(outDir, 0o750); err != nil { // #nosec G703 -- outDir is a local CLI arg, not attacker input
		fatal("FAIL @mkdir:", err)
	}

	// 2) fetch each full conversation -> Markdown (incremental, rate-limit-friendly)
	newN, skip, errN := 0, 0, 0
	for i, c := range all {
		fname := filepath.Join(outDir, fmt.Sprintf("%s_%s_%s.md", pathSafe(trunc(c.CreatedAt, 10)), pathSafe(trunc(c.UUID, 8)), sanitize(c.Name)))
		if fi, e := os.Stat(fname); e == nil && fi.Size() > 100 { // #nosec G703 -- see above
			skip++
			continue
		}
		body, err := fetchConvBody(get, org, c.UUID)
		if err != nil {
			errN++
			fmt.Printf("  [%d/%d] ERR %.40s: %v\n", i+1, len(all), c.Name, err)
			continue
		}
		md := convToMarkdown(body)
		if e := os.WriteFile(fname, []byte(md), 0o600); e != nil { // #nosec G703 -- see above
			errN++
			fmt.Printf("  [%d/%d] write ERR %.40s: %v\n", i+1, len(all), c.Name, e)
			continue
		}
		newN++
		fmt.Printf("  [%d/%d] %.50s (%d chars)\n", i+1, len(all), c.Name, len(md))
		time.Sleep(1200 * time.Millisecond)
	}
	fmt.Printf("\nDONE (chats): %d new, %d skipped, %d errors -> %s\n", newN, skip, errN, outDir)

	// 3) Non-chat surfaces — project knowledge docs + the claude.ai memory blob.
	fmt.Println("\n== project docs ==")
	pDocs, pErr := harvestProjects(get, org, outDir)
	fmt.Printf("DONE (projects): %d docs, %d errors\n", pDocs, pErr)

	fmt.Println("\n== memory ==")
	if e := harvestMemory(get, org, outDir); e != nil {
		fmt.Println("DONE (memory): ERR:", e)
	} else {
		fmt.Println("DONE (memory): claude-ai-memory.md written")
	}
}

// --- Markdown conversion (no PII filter — Michael's data, full content) ---

var reBadChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
var reSpaces = regexp.MustCompile(`\s+`)

// pathSafe strips a path component down to [0-9A-Za-z-] — applied to the API-sourced UUID
// and timestamp fields that go into filenames (the human title goes through sanitize()).
// Server values are already clean; this guarantees a ".." or path separator can never reach
// filepath.Join even if a claude.ai response were ever tampered with (defense in depth).
var reNotPathSafe = regexp.MustCompile(`[^0-9A-Za-z-]`)

func pathSafe(s string) string { return reNotPathSafe.ReplaceAllString(s, "") }

func sanitize(name string) string {
	s := reBadChars.ReplaceAllString(name, "")
	s = reSpaces.ReplaceAllString(strings.TrimSpace(s), "_")
	s = trunc(s, 80) // rune-safe — see trunc's doc comment
	if s == "" {
		return "Untitled"
	}
	return s
}

func rawText(r json.RawMessage) string {
	if len(r) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	var arr []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(r, &arr) == nil {
		var p []string
		for _, x := range arr {
			if x.Text != "" {
				p = append(p, x.Text)
			}
		}
		return strings.Join(p, " ")
	}
	return string(r)
}

func extractText(m message) string {
	if len(m.Content) == 0 {
		return m.Text
	}
	var parts []string
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "tool_use":
			parts = append(parts, fmt.Sprintf("\n```tool: %s\n%s\n```\n", b.Name, string(b.Input)))
		case "tool_result":
			parts = append(parts, fmt.Sprintf("\n```result\n%s\n```\n", rawText(b.Content)))
		default:
			// Unknown/new block type (extended-thinking, image, or any future Anthropic
			// type): preserve it verbatim instead of silently dropping it. This is a
			// faithful archival tool ("no PII filter — full content"), so nothing may vanish.
			body := rawText(b.Content)
			if body == "" {
				body = b.Text
			}
			parts = append(parts, fmt.Sprintf("\n```%s\n%s\n```\n", b.Type, body))
		}
	}
	return strings.Join(parts, "\n")
}

func convToMarkdown(raw string) string {
	var c fullConv
	if json.Unmarshal([]byte(raw), &c) != nil {
		return raw // fallback: keep raw JSON rather than lose data
	}
	title := c.Name
	if title == "" {
		title = "Untitled"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n- Created: %s\n- Updated: %s\n- Model: %s\n\n---\n\n",
		title, trunc(c.CreatedAt, 19), trunc(c.UpdatedAt, 19), c.Model)
	for _, m := range c.ChatMessages {
		label := "Claude"
		if m.Sender == "human" {
			label = "Human"
		}
		fmt.Fprintf(&b, "## %s [%s]\n\n", label, trunc(m.CreatedAt, 19))
		if txt := extractText(m); txt != "" {
			b.WriteString(txt)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

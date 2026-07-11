// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Security hardening: secret redaction + a static self-check that the code's own
// invariants (no headless override, no non-claude.ai network egress) still hold.
// See SECURITY.md for the full threat model this enforces.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

var (
	secretsMu sync.RWMutex
	secrets   []string
)

// RegisterSecret marks a decrypted value (the sessionKey; anything else this sensitive
// in the future) as something that must never reach stdout, the log file, or any report
// file. Call it the moment a secret is decrypted, before it's used for anything else.
// Short values are ignored — a 1-2 char match would redact unrelated ordinary text.
func RegisterSecret(s string) {
	if len(s) < 8 {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, existing := range secrets {
		if existing == s {
			return // already registered — readSessionKey re-registers the same value every
			// cycle in watch/tray mode; without this the slice would grow unboundedly over
			// a long-running daemon's lifetime instead of staying at a handful of entries.
		}
	}
	secrets = append(secrets, s)
}

// redact replaces every registered secret substring with a fixed placeholder. This is
// the single enforcement point: every output path in this program (stdout via
// installStdoutRedactor, the daemon's sync.log via logf, and probe-report.txt via
// probe.go's w() helper) routes through this function, so a secret can leak only if a
// caller reads os.Stdout's underlying fd directly — which nothing here does.
func redact(s string) string {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[REDACTED]")
		}
	}
	return s
}

// chromedpLogf routes chromedp's own internal log/error output through the same
// redaction the rest of this program's output gets. Unwired, chromedp defaults to Go's
// stdlib log.Printf (which targets os.Stderr) for BOTH its regular and error-level
// messages — entirely bypassing installStdoutRedactor below, which only ever wraps
// os.Stdout. Since network.Enable() (cdp.go) is active for the whole session, chromedp
// is processing full CDP network events — including the injected sessionKey cookie
// header — internally the entire time; an unexpected/malformed dispatch condition logs
// a raw protocol message via exactly this path (see chromedp's browser.go). Passed to
// chromedp.WithErrorf/WithLogf in cdp.go's openClaudeSession, which override chromedp's
// internal b.logf/b.errf fields directly (verified against chromedp v0.15.1 source).
func chromedpLogf(format string, args ...any) {
	fmt.Fprintln(os.Stderr, redact(fmt.Sprintf(format, args...)))
}

// installStdoutRedactor swaps os.Stdout for the write end of a pipe, scrubbing every
// byte written to it (by this program's own fmt.Print* calls, or by any third-party
// library that writes to stdout) before it reaches the real terminal. This is a single
// choke point — no call site anywhere in the program has to remember to redact, which
// matters because future code changes are the most likely way a redaction discipline
// silently rots. Call as `defer installStdoutRedactor()()` from main().
//
// Reads line-by-line (bufio.Reader.ReadString), not in fixed-size chunks: a secret
// straddling an arbitrary chunk boundary would match in neither half and leak.
// Secrets never contain newlines, so a full line always contains any secret intact
// before redact() runs on it.
func installStdoutRedactor() func() {
	real := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return func() {} // degrade to unredacted stdout rather than fail startup
	}
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReader(r)
		for {
			line, rerr := br.ReadString('\n')
			if len(line) > 0 {
				if _, werr := io.WriteString(real, redact(line)); werr != nil {
					return // real stdout is broken; nothing more we can do
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	return func() {
		_ = w.Close() // best-effort: we're tearing down, nothing to do with a Close error here
		<-done
		os.Stdout = real
	}
}

// stopRedactor drains and restores stdout. main() sets it to installStdoutRedactor()'s
// teardown so any fatal path can flush the async redaction pipe BEFORE os.Exit — os.Exit
// does not wait for goroutines, so without this the final diagnostic line printed on a
// failing run can be lost with the still-buffered pipe. Default no-op until main sets it.
var stopRedactor = func() {}

// activeTeardown holds the chromedp teardown for the currently-open Claude session, so a
// fatal exit — or the tray "Beenden" click, which fires on a *different* goroutine than the
// harvest that owns the open Chrome — can run it and not orphan the visible browser.
// openClaudeSession registers it; its own teardown clears it. Mutex-guarded because the
// tray daemon sets/reads it from two goroutines.
var (
	teardownMu     sync.Mutex
	activeTeardown func()
)

func registerTeardown(f func()) {
	teardownMu.Lock()
	activeTeardown = f
	teardownMu.Unlock()
}

func runActiveTeardown() {
	teardownMu.Lock()
	f := activeTeardown
	activeTeardown = nil
	teardownMu.Unlock()
	if f != nil {
		f()
	}
}

// fatal prints a message, tears down any open Chrome session, flushes the stdout redactor,
// then exits non-zero. Every fatal path routes through here so no os.Exit can (a) orphan a
// visible Chrome by skipping a deferred teardown, or (b) lose its final diagnostic line to
// the still-draining redaction pipe.
func fatal(a ...any) {
	fmt.Println(a...)
	runActiveTeardown()
	stopRedactor()
	os.Exit(1)
}

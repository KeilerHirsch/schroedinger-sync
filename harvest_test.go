// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Tests for the fetch-dependent logic using an in-memory mock `get` function — no browser,
// no credentials, no network. Covers org resolution (including the Cloudflare-challenge and
// multi-org paths), the retry happy path, and the memory-surface harvest/parse/write.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveOrg(t *testing.T) {
	if org, err := resolveOrg(func(string) (string, error) { return `[{"uuid":"ORG1"}]`, nil }); err != nil || org != "ORG1" {
		t.Errorf("single org: got %q err %v", org, err)
	}
	// A Cloudflare "Just a moment" HTML challenge must be reported as an error, never
	// mis-parsed into an org id.
	if _, err := resolveOrg(func(string) (string, error) { return `<html>Just a moment...</html>`, nil }); err == nil {
		t.Error("Cloudflare challenge body should error")
	}
	if _, err := resolveOrg(func(string) (string, error) { return `[]`, nil }); err == nil {
		t.Error("empty org list should error")
	}
	// Multi-org accounts harvest the first org (and log a warning); assert the returned id.
	if org, err := resolveOrg(func(string) (string, error) { return `[{"uuid":"A"},{"uuid":"B"}]`, nil }); err != nil || org != "A" {
		t.Errorf("multi-org: got %q err %v", org, err)
	}
}

func TestGetWithRetryReturnsBodyWhenOK(t *testing.T) {
	body, err := getWithRetry(func(string) (string, error) { return "OK-BODY", nil }, "/x", 3)
	if err != nil || body != "OK-BODY" {
		t.Errorf("got %q err %v", body, err)
	}
}

func TestHarvestMemory(t *testing.T) {
	dir := t.TempDir()
	get := func(string) (string, error) { return `{"memory":"# my memory blob"}`, nil }
	if _, err := harvestMemory(get, "ORG", dir); err != nil {
		t.Fatalf("harvestMemory: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "claude-ai-memory.md"))
	if err != nil {
		t.Fatalf("memory file not written: %v", err)
	}
	if !strings.Contains(string(b), "my memory blob") {
		t.Errorf("memory content missing: %s", b)
	}
	// An empty memory blob is a legitimate account state -> a no-op (nil), not an error
	// (M4 fix; TestHarvestMemoryEmptyIsNoop asserts it also writes nothing).
	if _, err := harvestMemory(func(string) (string, error) { return `{"memory":""}`, nil }, "ORG", dir); err != nil {
		t.Errorf("empty memory must be a no-op, got err %v", err)
	}
}

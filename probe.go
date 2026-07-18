// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Surface probe: empirically answer "does the harvest cover EVERYTHING on claude.ai,
// or only regular chats?".
//
// The harvest fetches exactly one resource: /api/organizations/{org}/chat_conversations.
// Newer claude.ai surfaces (Code, Cowork, Design, Projects) may either (a) show up in
// that same list under a type/product discriminator field — then they're already caught —
// or (b) live under their own endpoints — then they're MISSING from the sync.
//
// This command reads the real schema off YOUR account and probes candidate sibling
// endpoints, writing a plaintext report (probe-report.txt). No secrets are printed; the
// sessionKey is never emitted. Run it with Claude Desktop CLOSED (DPAPI cookie lock).
//
//	schroedinger-sync.exe probe
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func cdpProbe() {
	get, rawGet, teardown, err := openClaudeSession()
	if err != nil {
		exitOnSessionFailure(err)
	}
	defer teardown()

	org, err := resolveOrg(get)
	if err != nil {
		fatal("FAIL @org:", err)
	}

	var out strings.Builder
	w := func(format string, a ...any) {
		line := redact(fmt.Sprintf(format, a...))
		fmt.Println(line)
		out.WriteString(line + "\n")
	}

	w("# Schroedinger surface probe — %s", time.Now().Format(time.RFC3339))
	w("org_id: %s", org)

	// 1) Raw schema of the chat_conversations summaries. If a surface discriminator
	//    (type / product / is_code / conversation_type ...) exists, it shows up here.
	first, ferr := getWithRetry(get, "/api/organizations/"+org+"/chat_conversations?limit=3&offset=0", 5)
	if ferr != nil {
		w("\n## chat_conversations first page: FETCH ERR: %v", ferr)
	} else {
		w("\n## chat_conversations first page (raw, limit=3)\n%s", trunc(first, 6000))
	}

	var items []map[string]json.RawMessage
	if json.Unmarshal([]byte(first), &items) == nil && len(items) > 0 {
		keys := make([]string, 0, len(items[0]))
		for k := range items[0] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		w("\n## summary object keys: %v", keys)
	}

	// 2) Full count + tally of model + PLATFORM (the surface discriminator) + project tag.
	//    If Cowork/Code/Design conversations live in the same store, platform reveals them.
	total := 0
	models := map[string]int{}
	platforms := map[string]int{}
	withProject := 0
	const limit = 100
	for offset := 0; ; offset += limit {
		body, e := getWithRetry(get, fmt.Sprintf("/api/organizations/%s/chat_conversations?limit=%d&offset=%d", org, limit, offset), 6)
		if e != nil {
			w("count paginate error at offset %d: %v", offset, e)
			break
		}
		var page []struct {
			Model       string `json:"model"`
			Platform    string `json:"platform"`
			ProjectUUID string `json:"project_uuid"`
		}
		if json.Unmarshal([]byte(body), &page) != nil {
			break
		}
		for _, p := range page {
			models[p.Model]++
			platforms[p.Platform]++
			if p.ProjectUUID != "" {
				withProject++
			}
		}
		total += len(page)
		if len(page) < limit {
			break
		}
		time.Sleep(600 * time.Millisecond)
	}
	w("\n## total chat_conversations: %d", total)
	w("models: %v", models)
	w("platforms (SURFACE DISCRIMINATOR — cowork/code/design show here if in-store): %v", platforms)
	w("conversations tagged to a project: %d", withProject)

	// 3) Projects: list all, and probe the docs/knowledge sub-resource of the first one
	//    so we know the exact endpoint shape before writing the harvest.
	w("\n## projects (own resource — the project folders + their knowledge/docs)")
	projBody, perr := getWithRetry(get, "/api/organizations/"+org+"/projects", 3)
	var projs []struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	}
	if perr != nil {
		w("  projects FETCH ERR: %v", perr)
	} else if json.Unmarshal([]byte(projBody), &projs) != nil {
		w("  projects parse ERR: %s", trunc(projBody, 200))
	}
	w("  %d projects:", len(projs))
	for _, p := range projs {
		w("    - %-55.55s | %s", p.Name, p.UUID)
	}
	if len(projs) > 0 {
		for _, sub := range []string{"/docs", "/documents", ""} {
			st, body, _ := rawGet("/api/organizations/" + org + "/projects/" + projs[0].UUID + sub)
			w("  docs-probe sub=%-11q HTTP %d | %s", sub, st, trunc(strings.ReplaceAll(body, "\n", " "), 220))
			time.Sleep(300 * time.Millisecond)
		}
	}

	// 4) Memory (the claude.ai memory feature — the user's "30 slots"). Confirm size/shape.
	w("\n## memory endpoint")
	memSt, memBody, _ := rawGet("/api/organizations/" + org + "/memory")
	w("  /memory HTTP %d, body length=%d chars", memSt, len(memBody))
	w("  head: %s", trunc(strings.ReplaceAll(memBody, "\n", " "), 400))

	// defaultOutDir(), not CWD-relative: probe can be launched from a Start Menu icon,
	// Desktop shortcut, or Task Scheduler with an unpredictable working directory — the
	// same reasoning daemon.go's defaultOutDir doc comment lays out for every other
	// artifact this tool writes. A bare "probe-report.txt" used to scatter into whatever
	// folder happened to be current, and its content (org_id, conversation titles, project
	// names, a raw JSON page) is private even though the sessionKey itself is redacted.
	reportPath := filepath.Join(defaultOutDir(), "probe-report.txt")
	if err := os.MkdirAll(defaultOutDir(), 0o750); err != nil { // #nosec G304 G703 -- defaultOutDir is a fixed %LOCALAPPDATA% path, not variable input
		fmt.Println("(could not create data dir for probe-report.txt:", err, ")")
	} else if err := os.WriteFile(reportPath, []byte(out.String()), 0o600); err != nil { // #nosec G304 G703 -- see above
		fmt.Println("(could not write probe-report.txt:", err, ")")
	} else {
		fmt.Println("\nwrote", reportPath, "— paste it back or let Claude read it.")
	}
}

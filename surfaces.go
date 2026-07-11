// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Non-chat surfaces: project knowledge docs + the claude.ai memory blob.
//
// The probe established (2026-07-02) that claude.ai exposes, beyond chat_conversations:
//   - /api/organizations/{org}/projects            -> [{uuid,name,description,...}]
//   - /api/organizations/{org}/projects/{id}/docs  -> [{uuid,file_name,content,...}]
//   - /api/organizations/{org}/memory              -> {"memory":"<markdown>"}
//
// There is NO separate Cowork/Code/Design store — platform is only CLAUDE_AI/VOICE, and
// project chats already arrive via chat_conversations (project_uuid tag). So harvesting
// chats + project docs + memory captures everything reachable.
//
// Files are written flat into outDir (same dir the ingest pipeline mines), with distinct
// prefixes so no recursion is needed: project_<proj8>_<doc8>_<name>.md and
// claude-ai-memory.md.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type projectSummary struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type projectDoc struct {
	UUID      string `json:"uuid"`
	FileName  string `json:"file_name"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

// harvestProjects fetches every project and every project's knowledge docs, writing each
// doc as a Markdown file. Returns (#docs written, #errors).
func harvestProjects(get func(string) (string, error), org, outDir string) (docN, errN int) {
	body, err := getWithRetry(get, "/api/organizations/"+org+"/projects", 5)
	if err != nil {
		fmt.Println("  projects list ERR:", err)
		return 0, 1
	}
	var projs []projectSummary
	if json.Unmarshal([]byte(body), &projs) != nil {
		fmt.Println("  projects parse ERR:", trunc(body, 200))
		return 0, 1
	}
	fmt.Printf("  %d project(s)\n", len(projs))
	for _, p := range projs {
		docsBody, e := getWithRetry(get, "/api/organizations/"+org+"/projects/"+p.UUID+"/docs", 5)
		if e != nil {
			fmt.Printf("    [%s] docs ERR: %v\n", trunc(p.Name, 30), e)
			errN++
			continue
		}
		var docs []projectDoc
		if json.Unmarshal([]byte(docsBody), &docs) != nil {
			fmt.Printf("    [%s] docs parse ERR: %s\n", trunc(p.Name, 30), trunc(docsBody, 160))
			errN++
			continue
		}
		fmt.Printf("    [%s] %d doc(s)\n", trunc(p.Name, 40), len(docs))
		for _, d := range docs {
			fname := filepath.Join(outDir, fmt.Sprintf("project_%s_%s_%s",
				pathSafe(trunc(p.UUID, 8)), pathSafe(trunc(d.UUID, 8)), sanitize(strings.TrimSuffix(d.FileName, ".md"))))
			if !strings.HasSuffix(strings.ToLower(fname), ".md") {
				fname += ".md"
			}
			md := fmt.Sprintf("# %s\n\n- Project: %s (%s)\n- Doc UUID: %s\n- Created: %s\n\n---\n\n%s\n",
				d.FileName, p.Name, p.UUID, d.UUID, trunc(d.CreatedAt, 19), d.Content)
			if werr := os.WriteFile(fname, []byte(md), 0o600); werr == nil { // #nosec G703 -- outDir is a local CLI arg, see cdp.go
				docN++
			} else {
				errN++
				fmt.Printf("    [%s] write ERR %.30s: %v\n", trunc(p.Name, 30), d.FileName, werr)
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	return docN, errN
}

// harvestMemory writes the claude.ai memory blob to a single Markdown file.
func harvestMemory(get func(string) (string, error), org, outDir string) error {
	body, err := getWithRetry(get, "/api/organizations/"+org+"/memory", 5)
	if err != nil {
		return err
	}
	var m struct {
		Memory string `json:"memory"`
	}
	if json.Unmarshal([]byte(body), &m) != nil {
		return fmt.Errorf("parse memory: %s", trunc(body, 200))
	}
	if strings.TrimSpace(m.Memory) == "" {
		return fmt.Errorf("memory empty (body: %s)", trunc(body, 160))
	}
	fname := filepath.Join(outDir, "claude-ai-memory.md")
	md := fmt.Sprintf("# claude.ai Memory (org %s)\n\n- Harvested: %s\n\n---\n\n%s\n",
		org, time.Now().Format(time.RFC3339), m.Memory)
	return os.WriteFile(fname, []byte(md), 0o600) // #nosec G703 -- outDir is a local CLI arg, see cdp.go
}

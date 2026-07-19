// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Write-side half of the MemPalace ingest handshake (README roadmap #1). Every export
// write site in this program routes through writeMarkdown, which records the SHA-256 of
// what it just wrote in a per-outDir manifest — "verified rather than assumed" instead
// of a "should be lossless" claim. The read-back half (a re-hash from MemPalace's own
// store, compared against this manifest, reported as an X/Y scorecard) needs a change on
// the ingest side (mempalace-src) and is deliberately NOT built here — see CHANGELOG.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const manifestFileName = ".content-hashes.json"

// contentManifest maps an exported file's base name (not full path -- outDir is already
// the join key, same convention as syncState's UUID keys) to the SHA-256 hex of its
// content as of the last write.
type contentManifest map[string]string

// manifestMu guards loadManifest+saveManifest around each writeMarkdown call. The tray
// daemon's background sync goroutine and a manual harvest could in principle touch the
// same outDir concurrently (see security.go's activeTeardown for the same two-goroutine
// reasoning); a read-modify-write race here would silently drop one side's hash entry.
var manifestMu sync.Mutex

func manifestPath(outDir string) string { return filepath.Join(outDir, manifestFileName) }

// loadManifest never fails: a missing or corrupt manifest (first run, or an interrupted
// write from a version of this binary before this feature existed) falls back to an
// empty manifest, same as daemon.go's loadState does for .sync-state.json.
func loadManifest(outDir string) contentManifest {
	m := contentManifest{}
	b, err := os.ReadFile(manifestPath(outDir))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = contentManifest{}
	}
	return m
}

// saveManifest writes atomically via temp-file + rename, mirroring daemon.go's saveState
// (see its doc comment for why: a plain os.WriteFile can leave a truncated manifest on a
// crash or a race between two instances sharing an outDir).
func saveManifest(outDir string, m contentManifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(outDir, ".content-hashes-*.tmp") // #nosec G304 G703 -- outDir is a local CLI arg, see cdp.go
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, werr := f.Write(b); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return werr
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if err := os.Rename(tmp, manifestPath(outDir)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// hashContent returns the lowercase hex SHA-256 of content.
func hashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// writeMarkdown is the single choke point every export write site routes through (cdp.go
// cdpHarvest, daemon.go syncConversations, surfaces.go harvestProjects/harvestMemory) --
// same reasoning as security.go's redactor: one place, so recording the hash can never be
// forgotten at a call site. Deliberately load+save PER FILE rather than batching like
// syncState (which batches because it is a long-running daemon's cycle-level state): a
// harvest can be interrupted mid-way (context.DeadlineExceeded is an explicit, handled
// case elsewhere in this codebase), and a batched save would lose every hash for files
// already safely written before the interruption, leaving the manifest permanently out of
// sync with what is actually on disk. Per-file save costs one small JSON read+write per
// export (microseconds; manifests here top out at a few thousand entries) and keeps the
// manifest exactly as durable as the files it describes.
func writeMarkdown(outDir, fname string, content []byte) error {
	if err := os.WriteFile(fname, content, 0o600); err != nil { // #nosec G304 G703 -- outDir is a local CLI arg, see cdp.go
		return err
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	m := loadManifest(outDir)
	m[filepath.Base(fname)] = hashContent(content)
	if err := saveManifest(outDir, m); err != nil {
		// The Markdown file itself is already safely on disk -- a manifest write failure
		// (disk full, AV lock) must not be reported as if the export itself failed.
		logf("  manifest write ERR %.40s: %v", filepath.Base(fname), err)
	}
	return nil
}

package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestVbsLauncherContent proves the hand-rolled VBScript quote-escaping in
// vbsLauncherContent produces a launcher that WScript.Shell.Run would actually execute
// as the intended command line — not just "looks plausible on inspection", which is
// exactly how an earlier draft of this function shipped a bug (it used Go's %q instead
// of VBScript's own "" quote-doubling convention, which mangles backslashes in Windows
// paths instead of escaping quotes).
func TestVbsLauncherContent(t *testing.T) {
	cases := []struct {
		name        string
		exe, outDir string
		wantCmd     string // the command line WScript.Shell.Run should actually receive
	}{
		{
			name:    "no outDir",
			exe:     `C:\Program Files\schroedinger-sync.exe`,
			wantCmd: `"C:\Program Files\schroedinger-sync.exe" tray`,
		},
		{
			name:    "with outDir containing a space",
			exe:     `C:\Program Files\schroedinger-sync.exe`,
			outDir:  `C:\Users\Test\My Data\desktop-chats`,
			wantCmd: `"C:\Program Files\schroedinger-sync.exe" tray "C:\Users\Test\My Data\desktop-chats"`,
		},
		{
			// Proves the fix for the VBScript-injection bug: an outDir containing a
			// literal " must round-trip as data, not terminate the quoted argument early
			// and splice extra VBScript into the generated (unattended, logon-run) file.
			name:    "with outDir containing an embedded quote",
			exe:     `C:\Program Files\schroedinger-sync.exe`,
			outDir:  `C:\Users\Test\quo"ted\desktop-chats`,
			wantCmd: `"C:\Program Files\schroedinger-sync.exe" tray "C:\Users\Test\quo"ted\desktop-chats"`,
		},
	}
	runRe := regexp.MustCompile(`\.Run "(.*)", 0, False`)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content := vbsLauncherContent(c.exe, c.outDir)
			m := runRe.FindStringSubmatch(content)
			if m == nil {
				t.Fatalf("could not find a .Run(...) call in generated VBS:\n%s", content)
			}
			// VBScript de-escapes a doubled quote ("") to a single literal quote when it
			// parses a string literal — this mirrors exactly what the interpreter does,
			// giving us the actual argument .Run() would receive at runtime.
			gotCmd := strings.ReplaceAll(m[1], `""`, `"`)
			if gotCmd != c.wantCmd {
				t.Fatalf("Run() would execute %q, want %q", gotCmd, c.wantCmd)
			}
		})
	}
}

; Schroedinger Sync v2 — Inno Setup installer script.
;
; Why Inno Setup over WiX/NSIS: readable Pascal-like scripting, gentle learning curve,
; produces a standalone Setup.exe (not an MSI — we don't need GPO/enterprise deployment),
; and has first-class SignTool integration for later code signing. See SECURITY.md/README
; for the full comparison that led here.
;
; Why per-user, no admin: matches the rest of this project's "no admin needed" design
; (DPAPI reads, Startup-folder autostart) — PrivilegesRequired=lowest keeps it that way.
;
; Autostart reuses the app's own `install-task`/`uninstall-task` commands (see daemon.go)
; instead of Inno Setup's built-in startupicon mechanism, so there is exactly ONE place
; that knows how to register/remove the logon launcher — not two different mechanisms
; that could drift out of sync.
;
; Build: compile schroedinger-sync.exe first (see ../README.md), then:
;   "C:\Program Files (x86)\Inno Setup 6\ISCC.exe" schroedinger-sync.iss
; Output lands in installer\Output\SchroedingerSyncSetup.exe (gitignored).

#define MyAppName "Schroedinger Sync"
#define MyAppVersion "2.2.0"
#define MyAppPublisher "KeilerHirsch"
#define MyAppURL "https://github.com/KeilerHirsch-Labs/schroedinger-sync"
#define MyAppExeName "schroedinger-sync.exe"

[Setup]
AppId={{9591DD97-78F2-438C-B98B-473AD6505814}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}
DefaultDirName={localappdata}\SchroedingerSync
DefaultGroupName=Schroedinger Sync
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
LicenseFile=..\LICENSE
OutputDir=Output
OutputBaseFilename=SchroedingerSyncSetup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayIcon={app}\{#MyAppExeName}
; Inno Setup 6 defaults DisableWelcomePage to "yes" (confirmed against the offline 6.7.3
; help) -- without this, the classic Welcome page never shows, which made the new "A
; Different Kind of Sync" custom page effectively page 1 with no page before it to go
; Back to. Explicit "no" restores Welcome as page 1 and the pitch page as page 2, both
; with working Back navigation.
DisableWelcomePage=no

[Languages]
; Wizard-chrome strings (Next/Back/Cancel, License page labels, etc.) use Inno Setup's own
; official, professionally maintained .isl translations -- zero quality risk, safe to add
; broadly. Curated to the audience this repo's README actually names, not the full 28-file
; set Inno ships: German (existing -- the sharpest buyer wedges, §203 StGB/§393 SGB V, are
; German law) plus the rest of the EU/adjacent footprint the README's CLOUD-Act/KRITIS/NIS2
; argument targets. The CUSTOM terminal-page pitch text is deliberately English-only for
; every language (see CreateTerminalPage/PlayTerminalAnimation) -- Michael's call: it reads
; as a fixed brand moment, not localized UI chrome.
;
; LicenseFile per language: the actual AGPL-3.0 legal text is NOT translated in-house here.
; The FSF's own official translations list (gnu.org/licenses/translations.html, checked
; 2026-07-19) covers AGPL-3.0 for only 8 languages total, and of the ones added below only
; Russian and Brazilian Portuguese are on it -- no German, French, Spanish, Italian, Dutch,
; or Polish AGPL-3.0 translation is FSF-linked. Shipping a self-made translation of a
; copyleft license's actual legal terms is exactly the kind of error this project's
; law-firm-facing audience would catch immediately, so those languages deliberately show
; the LicenseFile in the [Setup] section's own English default rather than an unvetted
; translation. Both files used here retain the mandatory "unofficial, English governs"
; disclaimer FSF requires for permission to redistribute at all (see each file's header).
Name: "english"; MessagesFile: "compiler:Default.isl"
Name: "german"; MessagesFile: "compiler:Languages\German.isl"
Name: "french"; MessagesFile: "compiler:Languages\French.isl"
Name: "spanish"; MessagesFile: "compiler:Languages\Spanish.isl"
Name: "italian"; MessagesFile: "compiler:Languages\Italian.isl"
Name: "dutch"; MessagesFile: "compiler:Languages\Dutch.isl"
Name: "brazilianportuguese"; MessagesFile: "compiler:Languages\BrazilianPortuguese.isl"; LicenseFile: "license-BrazilianPortuguese.txt"
Name: "polish"; MessagesFile: "compiler:Languages\Polish.isl"
Name: "russian"; MessagesFile: "compiler:Languages\Russian.isl"; LicenseFile: "license-Russian.txt"

[Tasks]
; Tasks are checked by default unless marked "unchecked" — no "checked" flag exists in
; this section (that's an [Icons]-only flag; using it here is what broke the compile).
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"
Name: "startuptray"; Description: "Bei Windows-Anmeldung starten (Tray-Icon)"; GroupDescription: "Autostart"

[Files]
Source: "..\schroedinger-sync.exe"; DestDir: "{app}"; DestName: "{#MyAppExeName}"; Flags: ignoreversion
Source: "..\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\SECURITY.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Parameters: "tray"
Name: "{group}\{cm:UninstallProgram,{#MyAppName}}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Parameters: "tray"; Tasks: desktopicon

[Run]
; Register the logon-autostart launcher via the app's own command, not a duplicate
; Inno Setup mechanism — see header comment.
Filename: "{app}\{#MyAppExeName}"; Parameters: "install-task"; Flags: runhidden; Tasks: startuptray
Filename: "{app}\{#MyAppExeName}"; Parameters: "tray"; Description: "Schroedinger Sync jetzt starten"; Flags: postinstall nowait skipifsilent unchecked runhidden

[UninstallRun]
; Remove the logon-autostart launcher before the exe it points to disappears.
; RunOnceId ensures this only ever executes once per uninstall, even if the uninstaller
; is invoked in stages (e.g. a repair-then-remove flow).
Filename: "{app}\{#MyAppExeName}"; Parameters: "uninstall-task"; Flags: runhidden; RunOnceId: "RemoveAutostart"

; Sweep runtime residue this program creates OUTSIDE {app} (so Inno Setup's own
; per-{app}-folder cleanup never reaches it): the chrome-profile tree
; ({localappdata}\SchroedingerSync\chrome-profile) and any leftover
; {tmp-root}\schroedinger_* copyCookieDB temp dir from a crashed run. Must run before
; uninstall-task removed the exe above only in the sense that both need the exe present —
; order between the two doesn't otherwise matter, so this is a second, independent
; RunOnceId, not chained to RemoveAutostart.
Filename: "{app}\{#MyAppExeName}"; Parameters: "cleanup-temp"; Flags: runhidden; RunOnceId: "CleanupTemp"

; Deliberately NOT removing the user's harvested data (desktop-chats\ under wherever
; they pointed the daemon) on uninstall — that's their exported private conversation
; history, not installer state. Only the program files, autostart entry, and this
; program's own crash-residue temp locations are removed.

[Code]
// --- "A Different Kind of Sync" page: a live-typed terminal-style pitch shown once, right
// after Welcome. Deliberately a real custom page rather than a static WizardImageFile: a
// static image cannot type itself, and Michael's explicit ask was that the pitch feel like
// it's actually being written in front of the user, not a screenshot. Text is the
// confidentiality-forward variant (targets the two sharpest buyer wedges from the README --
// law firms/§203 StGB, healthcare/§393 SGB V+BSI C5 -- without naming either on a welcome
// screen, that belongs in the README). "beyond compliance, by design" deliberately makes no
// claim about a specific security mechanism (the TPM/CNG envelope encryption in the README
// roadmap is NOT shipped yet) -- it states a design stance that is already true today
// (zero network calls), not a feature that isn't built.
var
  TerminalPage: TWizardPage;
  TermCmd: TNewStaticText;
  TermStat1, TermStat2, TermStat3, TermStat4: TNewStaticText;
  TermFinal1, TermFinal2: TNewStaticText;

// Inno Setup's Pascal Script has no Application.ProcessMessages -- Sleep() is a raw block,
// confirmed against the offline Inno Setup 6.7.3 help (topic_isxfunc_sleep.htm has no
// message-pump remark, and ProcessMessages doesn't exist anywhere in that CHM at all; the
// compiler's own "Unknown identifier 'Application'" on the first draft confirmed it live).
// TControl.Repaint (Invalidate+Update, synchronous) is what actually forces the changed
// Caption on screen before the following Sleep. Because Sleep never yields to the message
// queue, a Next/Cancel click during the ~3s animation cannot land mid-loop at all -- Windows
// simply won't dispatch it until this call stack unwinds -- so there is nothing for a
// current-page guard to protect against; not adding one is deliberate, not an oversight.

// Character-at-a-time reveal at a deliberately unhurried, slightly irregular pace -- a
// uniform interval reads as a machine, not a person typing.
procedure TypeInto(Lbl: TNewStaticText; const Text: String);
var
  I, Delay: Integer;
  Ch: String;
begin
  Lbl.Caption := '';
  for I := 1 to Length(Text) do begin
    Ch := Copy(Text, I, 1);
    Lbl.Caption := Lbl.Caption + Ch;
    Lbl.Repaint;
    Delay := 62 + Random(55);
    if Ch = ' ' then Delay := Delay + 70 + Random(60);
    if Random(100) < 6 then Delay := Delay + 140 + Random(180);
    Sleep(Delay);
  end;
end;

procedure RevealLine(Lbl: TNewStaticText; const Text: String; PauseAfterMs: Integer);
begin
  Lbl.Caption := Text;
  Lbl.Visible := True;
  Lbl.Repaint;
  Sleep(PauseAfterMs);
end;

// The terminal pitch is deliberately English-only regardless of the active wizard
// language (Michael's explicit call, 2026-07-19): it reads as a fixed brand moment --
// like a wordmark or tagline -- not as UI chrome that should follow the user's language,
// and English is the one language every audience segment (individuals, German-law firms,
// EU healthcare/KRITIS operators) shares. The page Caption/Description below the wizard
// chrome stay in the active language for consistency with every other page's title bar.
procedure PlayTerminalAnimation();
begin
  TypeInto(TermCmd, 'schroedinger sync --all');
  Sleep(150);
  RevealLine(TermStat1, 'conversations       captured', 190);
  RevealLine(TermStat2, 'client data         stays here', 190);
  RevealLine(TermStat3, 'cloud dependency    none', 190);
  RevealLine(TermStat4, 'audit trail         on your disk', 260);
  RevealLine(TermFinal1, 'nothing leaves this machine', 260);
  RevealLine(TermFinal2, 'beyond compliance, by design', 0);
end;

// Pascal Script (unlike full Delphi) does not support nested routines inside another
// procedure's var block, so this stays top-level rather than local to CreateTerminalPage.
function MakeTermLine(AParent: TWinControl; ATop: Integer; AColor: TColor): TNewStaticText;
begin
  Result := TNewStaticText.Create(TerminalPage);
  Result.Parent := AParent;
  Result.Left := 18;
  Result.Top := ATop;
  Result.AutoSize := True;
  Result.Font.Name := 'Consolas';
  Result.Font.Size := 9;
  Result.Font.Color := AColor;
  Result.Caption := '';
  Result.Visible := False;
end;

procedure CreateTerminalPage();
var
  PromptLbl: TNewStaticText;
  Y, LineH: Integer;
  German: Boolean;
begin
  German := ActiveLanguage = 'german';
  if German then
    TerminalPage := CreateCustomPage(wpWelcome, 'Synchronisation – neu gedacht',
      'Was in diesem Moment wirklich passiert.')
  else
    TerminalPage := CreateCustomPage(wpWelcome, 'A Different Kind of Sync',
      'What happens the moment you install this.');

  // TPanel's background paint goes through Windows' themed "parent background" renderer
  // and ignores an explicit .Color under Windows visual styles (confirmed live: a first
  // draft using a TPanel here rendered its labels' own background correctly -- they inherit
  // .Color via ParentColor, which is a plain property read, not theme-drawn -- while the
  // panel's own large fill stayed white). TNewNotebookPage (a TCustomControl, not a themed
  // control) paints .Color directly and reliably, so the page Surface itself IS the dark
  // panel; no intermediate TPanel.
  TerminalPage.Surface.Color := $1A0A0D; // BGR -- matches the project's #0d0a1a void

  LineH := 20;
  Y := 16;

  PromptLbl := TNewStaticText.Create(TerminalPage);
  PromptLbl.Parent := TerminalPage.Surface;
  PromptLbl.Left := 18;
  PromptLbl.Top := Y;
  PromptLbl.AutoSize := True;
  PromptLbl.Font.Name := 'Consolas';
  PromptLbl.Font.Size := 9;
  PromptLbl.Font.Color := $50B93F; // BGR -- matches #3fb950
  PromptLbl.Caption := '$ ';

  TermCmd := TNewStaticText.Create(TerminalPage);
  TermCmd.Parent := TerminalPage.Surface;
  TermCmd.Left := PromptLbl.Left + 16;
  TermCmd.Top := Y;
  TermCmd.AutoSize := True;
  TermCmd.Font.Name := 'Consolas';
  TermCmd.Font.Size := 9;
  TermCmd.Font.Color := $F3EDE6; // BGR -- matches #e6edf3
  TermCmd.Caption := '';
  Y := Y + LineH + 8;

  TermStat1 := MakeTermLine(TerminalPage.Surface, Y, $9E948B); Y := Y + LineH; // BGR -- #8b949e
  TermStat2 := MakeTermLine(TerminalPage.Surface, Y, $9E948B); Y := Y + LineH;
  TermStat3 := MakeTermLine(TerminalPage.Surface, Y, $9E948B); Y := Y + LineH;
  TermStat4 := MakeTermLine(TerminalPage.Surface, Y, $9E948B); Y := Y + LineH + 6;

  TermFinal1 := MakeTermLine(TerminalPage.Surface, Y, $50B93F); Y := Y + LineH; // #3fb950
  TermFinal2 := MakeTermLine(TerminalPage.Surface, Y, $FFA658); // BGR -- #58a6ff
end;

// Plays once: CurPageChanged only fires when Inno Setup actually navigates TO this page,
// so arriving here via Back later would replay it too -- accepted as a feature (the pitch
// is short enough that a replay on Back is not an annoyance) rather than added complexity
// to suppress it.
procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID = TerminalPage.ID then
    PlayTerminalAnimation();
end;

procedure InitializeWizard();
begin
  CreateTerminalPage();
end;

// Before a (re)install lays down a new schroedinger-sync.exe, ask whichever version is
// CURRENTLY installed to remove its own autostart entry + runtime residue first — same
// two commands [UninstallRun] uses, just run pre-install instead of only at uninstall.
// Matters because DefaultDirName/AppId never changed across versions, so an upgrade
// silently reuses {app}: without this, residue from the version being replaced (a stale
// autostart launcher pointing at soon-to-be-overwritten bytes, an orphaned chrome-profile
// subdir) would sit next to the new install indefinitely instead of only ever being swept
// reactively on the new exe's first daemon start.
//
// Delegated to the OLD exe rather than reimplemented here: uninstallTask/cleanupTemp
// (daemon.go) already carry the safety logic this needs (PID-liveness check before
// deleting a chrome-profile dir, reparse-point refusal, age-gated temp-copy sweep) —
// redoing that in Pascal would duplicate and risk drifting from logic that's already
// reviewed and tested. Known gap: cleanup-temp itself is new in this same release, so
// upgrading FROM a version older than this one silently no-ops on that one call (the old
// exe doesn't recognise the command) — harmless (fail-open, unchecked exit code, matches
// [UninstallRun]'s own pattern), and not a regression: the new exe's cleanup-temp already
// runs automatically on its own first watch/supervise/tray start regardless, so the same
// residue still gets swept, just on first run instead of strictly pre-install. From this
// version onward, every future upgrade gets the full pre-install sweep.
procedure CleanupPreviousInstallResidue();
var
  OldExe: String;
  ResultCode: Integer;
begin
  OldExe := ExpandConstant('{app}\{#MyAppExeName}');
  if not FileExists(OldExe) then
    Exit; // fresh install — no previous version at {app} to clean up after

  Exec(OldExe, 'uninstall-task', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec(OldExe, 'cleanup-temp', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  // ssInstall fires "just before the actual installation starts" (Inno Setup docs) — i.e.
  // strictly before [Files] copies the new exe over whatever is currently at {app}, so the
  // previous version's binary is still intact and callable here.
  if CurStep = ssInstall then
    CleanupPreviousInstallResidue();
end;

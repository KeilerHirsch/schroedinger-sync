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
#define MyAppVersion "2.1.1"
#define MyAppPublisher "KeilerHirsch"
#define MyAppURL "https://github.com/KeilerHirsch/schroedinger-sync"
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

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"
Name: "german"; MessagesFile: "compiler:Languages\German.isl"

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

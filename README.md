# xcode-sync

Pull selected Xcode preferences and editor customizations from one Mac to
another.

Each operation is deliberately one-way: the local Mac pulls from the source Mac
you name. Both Macs must have the same Xcode and `xcode-sync` versions.

## Setup

Install `xcode-sync` on the source and target Macs and make sure the target can
reach the source over SSH. The source may be a hostname or an alias from
`~/.ssh/config`.

The remote command is resolved through the source Mac's login-shell `PATH`, so a
normal Homebrew shell setup works without a fixed installation path. Use
`--source-binary <command-or-absolute-path>` for a nonstandard installation.

SSH defaults to the local `$USER`. Override it with `--user <user>` or
`-u <user>`.

## Usage

Preview a pull. This reads both Macs but writes no local state:

```sh
xcode-sync pull source-mac --dry-run
```

Quit Xcode on the target, then apply it:

```sh
xcode-sync pull source-mac
```

Other commands:

```sh
xcode-sync status source-mac  # exit 1 when changes are available
xcode-sync audit              # validate and summarize local sync data
xcode-sync export             # print the sanitized source bundle
xcode-sync rollback           # restore the latest completed backup
```

Xcode UserData defaults to `~/Library/Developer/Xcode/UserData`. Backups default
to `$XDG_STATE_HOME/xcode-sync/backups`, or
`~/.local/state/xcode-sync/backups` when `XDG_STATE_HOME` is unset.

Use `--user-data`, `--source-user-data`, and `--state-home` with absolute paths
to override those locations. SSH connections time out after `10s` and exports
after `1m`; override them with `--ssh-connect-timeout` and `--export-timeout`.

## What gets synced

The complete set of custom files with these extensions:

- `FontAndColorThemes/*.xccolortheme` — colors, fonts, and font sizes
- `KeyBindings/*.idekeybindings` — custom Xcode key bindings
- `CodeSnippets/*.codesnippet` — user code snippets

And this closed allowlist from the `com.apple.dt.Xcode` preferences domain:

- selected light, dark, and legacy font/color themes
- selected key-binding set
- predictive completion
- minimap visibility
- trim whitespace-only lines
- automatic String Catalog comments
- suppress clean-build prompt

Missing values are synced as missing, so a target-only override is reset to the
Xcode default. Target-only managed files are removed. Unrelated files and
preferences are left untouched.

Accounts, Apple IDs, signing teams, certificates, source-control accounts,
recent projects and documents, file paths, devices, simulators, window state,
downloaded platforms, and analytics are never exported.

## Safety

`xcode-sync` validates the bundle schema, content hash, file names, sizes,
permissions, tool version, and Xcode version before applying it. Diffs contain
only preference names and file paths, never preference values or file contents.

Pulls and rollbacks refuse to run while Xcode is open. A pull captures the full
local sync scope before changing anything, stores it in a private backup, writes
files atomically, and restores the backup if an apply step fails. Concurrent
write operations are rejected.

Backups include the full contents of custom themes, key bindings, and snippets;
treat them as private.

## Adding a preference

Xcode's preference keys are not a stable public API. Confirm a new key with a
one-setting before/after comparison, check that it contains no account,
machine, project, or path-specific data, then add its exact type to
`preferenceSpecs` with export, apply, and rollback tests. Never sync the whole
`com.apple.dt.Xcode` domain.

## Development

```sh
go test ./...
make build
make prod
```

Tests use temporary directories and fake `defaults` commands; they do not touch
live Xcode preferences.

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tombell/xcode-sync/internal/syncx"
)

var (
	Version = "dev"
	Commit  = "unknown"
)

type options struct {
	UserData       string
	StateHome      string
	SourceUserData string
	SourceBinary   string
	SSHUser        string
	SSHConnect     time.Duration
	ExportTimeout  time.Duration
	DryRun         bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	args, opts, err := parseOptions(args)
	if err != nil {
		fmt.Fprintf(stderr, "xcode-sync: %v\n", err)
		return 2
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Fprintf(stdout, "xcode-sync %s (%s)\n", Version, Commit)
		return 0
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printUsage(stdout)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	layout, err := syncx.LiveLayout(opts.UserData, opts.StateHome)
	if err != nil {
		fmt.Fprintf(stderr, "xcode-sync: %v\n", err)
		return 2
	}
	unlock := func() {}
	if args[0] == "rollback" || (args[0] == "pull" && !opts.DryRun) {
		unlock, err = syncx.OperationLock(layout)
		if err != nil {
			fmt.Fprintf(stderr, "xcode-sync: %v\n", err)
			return 2
		}
	}
	defer unlock()

	code, err := runCommand(ctx, args, opts, layout, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "xcode-sync: %v\n", err)
		return 2
	}
	return code
}

func runCommand(ctx context.Context, args []string, opts options, layout syncx.Layout, stdout io.Writer) (int, error) {
	switch args[0] {
	case "export":
		if len(args) != 1 {
			return 0, fmt.Errorf("usage: xcode-sync export")
		}
		content, version, err := localSnapshot(ctx, layout)
		if err != nil {
			return 0, err
		}
		bundle, err := syncx.NewBundle(content, Version, version, "source", time.Now())
		if err != nil {
			return 0, err
		}
		data, err := syncx.EncodeBundle(bundle)
		if err != nil {
			return 0, err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", data)
		return 0, err
	case "audit":
		if len(args) != 1 {
			return 0, fmt.Errorf("usage: xcode-sync audit")
		}
		content, version, err := localSnapshot(ctx, layout)
		if err != nil {
			return 0, err
		}
		present := 0
		for _, entry := range content.Preferences {
			if entry.Present {
				present++
			}
		}
		fmt.Fprintf(stdout, "Xcode: %s\nPortable preferences: %d\nManaged files: %d\n", oneLine(version), present, len(content.Files))
		return 0, nil
	case "status", "pull":
		if len(args) != 2 {
			return 0, fmt.Errorf("usage: xcode-sync %s <host>", args[0])
		}
		bundle, err := syncx.FetchBundle(ctx, syncx.RemoteOptions{
			Host: args[1], User: opts.SSHUser, Binary: opts.SourceBinary,
			SourceUserData: opts.SourceUserData, ConnectTimeout: opts.SSHConnect,
			ExportTimeout: opts.ExportTimeout,
		})
		if err != nil {
			return 0, err
		}
		current, version, err := localSnapshot(ctx, layout)
		if err != nil {
			return 0, err
		}
		if err := syncx.ValidateBundle(bundle, Version, version); err != nil {
			return 0, err
		}
		if bundle.Manifest.SourceRole != "source" {
			return 0, fmt.Errorf("remote bundle has invalid role %q", bundle.Manifest.SourceRole)
		}
		changes := syncx.Compare(current, bundle.Content)
		printChanges(stdout, changes)
		if opts.DryRun {
			fmt.Fprintln(stdout, "Dry run only; no local settings or backups were written.")
			return 0, nil
		}
		if args[0] == "status" {
			if len(changes) > 0 {
				return 1, nil
			}
			return 0, nil
		}
		if len(changes) == 0 {
			return 0, nil
		}
		running, err := syncx.XcodeRunning(ctx, syncx.RunCommand)
		if err != nil {
			return 0, err
		}
		if running {
			return 0, fmt.Errorf("Xcode is running; quit it before applying settings")
		}
		backup, err := syncx.ApplyWithBackup(ctx, layout, bundle.Content, Version, version, syncx.RunCommand, time.Now())
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(stdout, "Applied settings. Backup: %s\nRollback with: xcode-sync rollback\n", backup)
		return 0, nil
	case "rollback":
		if len(args) != 1 {
			return 0, fmt.Errorf("usage: xcode-sync rollback")
		}
		running, err := syncx.XcodeRunning(ctx, syncx.RunCommand)
		if err != nil {
			return 0, err
		}
		if running {
			return 0, fmt.Errorf("Xcode is running; quit it before rollback")
		}
		version, err := syncx.XcodeVersion(ctx, syncx.RunCommand)
		if err != nil {
			return 0, err
		}
		path, err := syncx.Rollback(ctx, layout, Version, version, syncx.RunCommand)
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(stdout, "Restored backup: %s\n", path)
		return 0, nil
	default:
		return 0, fmt.Errorf("unknown command %q", args[0])
	}
}

func localSnapshot(ctx context.Context, layout syncx.Layout) (syncx.Content, string, error) {
	version, err := syncx.XcodeVersion(ctx, syncx.RunCommand)
	if err != nil {
		return syncx.Content{}, "", err
	}
	content, err := syncx.BuildContent(ctx, layout, syncx.RunCommand)
	return content, version, err
}

func parseOptions(args []string) ([]string, options, error) {
	opts := options{
		SSHUser: os.Getenv("USER"), SourceBinary: "xcode-sync",
		SSHConnect: 10 * time.Second, ExportTimeout: time.Minute,
	}
	remaining := make([]string, 0, len(args))
	dryRun := false
	seen := make(map[string]bool)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--dry-run" {
			if dryRun {
				return nil, options{}, fmt.Errorf("--dry-run may only be specified once")
			}
			dryRun = true
			continue
		}
		name, inline, hasInline := strings.Cut(argument, "=")
		var destination *string
		switch name {
		case "--user-data":
			destination = &opts.UserData
		case "--state-home":
			destination = &opts.StateHome
		case "--source-user-data":
			destination = &opts.SourceUserData
		case "--source-binary":
			destination = &opts.SourceBinary
		case "--user", "-u":
			destination = &opts.SSHUser
		}
		if destination != nil {
			canonicalName := name
			if canonicalName == "-u" {
				canonicalName = "--user"
			}
			if seen[canonicalName] {
				return nil, options{}, fmt.Errorf("%s may only be specified once", canonicalName)
			}
			seen[canonicalName] = true
			value := inline
			if !hasInline {
				index++
				if index == len(args) {
					return nil, options{}, fmt.Errorf("%s requires a value", name)
				}
				value = args[index]
			}
			if value == "" {
				return nil, options{}, fmt.Errorf("%s requires a value", name)
			}
			*destination = value
			continue
		}
		var duration *time.Duration
		switch name {
		case "--ssh-connect-timeout":
			duration = &opts.SSHConnect
		case "--export-timeout":
			duration = &opts.ExportTimeout
		}
		if duration != nil {
			if seen[name] {
				return nil, options{}, fmt.Errorf("%s may only be specified once", name)
			}
			seen[name] = true
			value := inline
			if !hasInline {
				index++
				if index == len(args) {
					return nil, options{}, fmt.Errorf("%s requires a duration", name)
				}
				value = args[index]
			}
			parsed, err := time.ParseDuration(value)
			if err != nil || parsed < time.Second || parsed > time.Hour || parsed%time.Second != 0 {
				return nil, options{}, fmt.Errorf("%s requires whole seconds from 1s to 1h", name)
			}
			*duration = parsed
			continue
		}
		remaining = append(remaining, argument)
	}
	for _, path := range []struct{ name, value string }{
		{"--user-data", opts.UserData}, {"--state-home", opts.StateHome}, {"--source-user-data", opts.SourceUserData},
	} {
		if path.value != "" && !filepath.IsAbs(path.value) {
			return nil, options{}, fmt.Errorf("%s requires an absolute path", path.name)
		}
	}
	if dryRun {
		if len(remaining) != 2 || remaining[0] != "pull" {
			return nil, options{}, fmt.Errorf("--dry-run is only valid with pull")
		}
		opts.DryRun = true
	}
	if len(remaining) > 0 && remaining[0] != "pull" && remaining[0] != "status" {
		for _, name := range []string{"--user", "--source-user-data", "--source-binary", "--ssh-connect-timeout", "--export-timeout"} {
			if seen[name] {
				return nil, options{}, fmt.Errorf("%s is only valid with pull or status", name)
			}
		}
	}
	return remaining, opts, nil
}

func printChanges(output io.Writer, changes []syncx.Change) {
	if len(changes) == 0 {
		fmt.Fprintln(output, "Already in sync.")
		return
	}
	fmt.Fprintf(output, "%d change(s):\n", len(changes))
	for _, change := range changes {
		fmt.Fprintf(output, "  %s: %s\n", change.Kind, change.Name)
	}
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, `Usage:
  xcode-sync export
  xcode-sync audit
  xcode-sync status <host> [--user <user>]
  xcode-sync pull <host> [--user <user>] [--dry-run]
  xcode-sync rollback

Options:
  --user-data <absolute-path>          Override local Xcode UserData
  --state-home <absolute-path>         Override backup state root
  --source-user-data <absolute-path>   Override source Xcode UserData
  --source-binary <command-or-path>    Override remote xcode-sync command
  --ssh-connect-timeout <duration>     Default: 10s
  --export-timeout <duration>          Default: 1m
  --version                            Print version`)
}

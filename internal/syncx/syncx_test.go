package syncx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeDefaults struct {
	preferences map[string]PreferenceEntry
	missingText string
}

func newFakeDefaults(entries map[string]PreferenceEntry) *fakeDefaults {
	copy := make(map[string]PreferenceEntry, len(entries))
	for name, entry := range entries {
		copy[name] = entry
	}
	return &fakeDefaults{preferences: copy, missingText: "does not exist"}
}

func (fake *fakeDefaults) run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "xcodebuild" && reflect.DeepEqual(args, []string{"-version"}) {
		return []byte("Xcode 26.6\nBuild version 17F113\n"), nil
	}
	if name != "defaults" || len(args) < 3 || args[1] != preferencesDomain {
		return nil, errors.New("unexpected command")
	}
	command, key := args[0], args[2]
	entry, present := fake.preferences[key]
	switch command {
	case "read-type":
		if !present || !entry.Present {
			return []byte(fake.missingText), errors.New("exit status 1")
		}
		if preferenceSpecs[key] == preferenceBoolean {
			return []byte("Type is boolean\n"), nil
		}
		return []byte("Type is string\n"), nil
	case "read":
		if !present || !entry.Present {
			return []byte(fake.missingText), errors.New("exit status 1")
		}
		if value, ok := entry.Value.(bool); ok {
			if value {
				return []byte("1\n"), nil
			}
			return []byte("0\n"), nil
		}
		return []byte(entry.Value.(string) + "\n"), nil
	case "delete":
		if !present || !entry.Present {
			return []byte(fake.missingText), errors.New("exit status 1")
		}
		fake.preferences[key] = PreferenceEntry{Present: false}
		return nil, nil
	case "write":
		if len(args) != 5 {
			return nil, errors.New("invalid defaults write")
		}
		if args[3] == "-bool" {
			fake.preferences[key] = PreferenceEntry{Present: true, Value: args[4] == "true"}
		} else {
			fake.preferences[key] = PreferenceEntry{Present: true, Value: args[4]}
		}
		return nil, nil
	default:
		return nil, errors.New("unexpected defaults operation")
	}
}

func TestBuildContentUsesClosedAllowlistAndManagedExtensions(t *testing.T) {
	layout := testLayout(t)
	writeTestFile(t, filepath.Join(layout.UserData(), "FontAndColorThemes", "Night.xccolortheme"), "theme")
	writeTestFile(t, filepath.Join(layout.UserData(), "FontAndColorThemes", "notes.txt"), "private")
	writeTestFile(t, filepath.Join(layout.UserData(), "KeyBindings", "Mine.idekeybindings"), "keys")
	prefs := blankPreferences()
	prefs["DVTTextShowMinimap"] = PreferenceEntry{Present: true, Value: false}
	prefs["XCFontAndColorCurrentDarkTheme"] = PreferenceEntry{Present: true, Value: "Night.xccolortheme"}
	fake := newFakeDefaults(prefs)

	content, err := BuildContent(context.Background(), layout, fake.run)
	if err != nil {
		t.Fatal(err)
	}
	if len(content.Preferences) != len(preferenceSpecs) {
		t.Fatalf("got %d preferences, want %d", len(content.Preferences), len(preferenceSpecs))
	}
	if _, ok := content.Files[filepath.Join("FontAndColorThemes", "Night.xccolortheme")]; !ok {
		t.Fatal("theme was not exported")
	}
	if _, ok := content.Files[filepath.Join("KeyBindings", "Mine.idekeybindings")]; !ok {
		t.Fatal("key bindings were not exported")
	}
	if _, ok := content.Files[filepath.Join("FontAndColorThemes", "notes.txt")]; ok {
		t.Fatal("unmanaged file was exported")
	}
}

func TestBundleValidationRejectsTamperingAndTrailingJSON(t *testing.T) {
	content := Content{Preferences: blankPreferences(), Files: map[string]FileEntry{
		filepath.Join("CodeSnippets", "Print.codesnippet"): {Data: []byte("snippet"), Mode: 0600},
	}}
	bundle, err := NewBundle(content, "1.0.0", "Xcode 26.6", "source", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBundle(bundle, "1.0.0", "Xcode 26.6"); err != nil {
		t.Fatal(err)
	}
	bundle.Content.Files[filepath.Join("CodeSnippets", "Print.codesnippet")] = FileEntry{Data: []byte("changed"), Mode: 0600}
	if err := ValidateBundle(bundle, "1.0.0", "Xcode 26.6"); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("expected hash error, got %v", err)
	}
	data, err := EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBundle(append(data, []byte(" {}")...)); err == nil {
		t.Fatal("expected trailing JSON to be rejected")
	}
}

func TestApplyWithBackupAndRollback(t *testing.T) {
	layout := testLayout(t)
	oldTheme := filepath.Join(layout.UserData(), "FontAndColorThemes", "Old.xccolortheme")
	writeTestFile(t, oldTheme, "old")
	writeTestFile(t, filepath.Join(layout.UserData(), "FontAndColorThemes", "README.txt"), "keep")
	initial := blankPreferences()
	initial["DVTTextShowMinimap"] = PreferenceEntry{Present: true, Value: false}
	fake := newFakeDefaults(initial)

	wanted := Content{Preferences: blankPreferences(), Files: map[string]FileEntry{
		filepath.Join("FontAndColorThemes", "New.xccolortheme"): {Data: []byte("new"), Mode: 0600},
	}}
	wanted.Preferences["DVTTextShowMinimap"] = PreferenceEntry{Present: true, Value: true}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	backup, err := ApplyWithBackup(context.Background(), layout, wanted, "1.0.0", "Xcode 26.6\nBuild version 17F113", fake.run, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if _, err := os.Stat(oldTheme); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old theme was not removed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(layout.UserData(), "FontAndColorThemes", "New.xccolortheme")); err != nil || string(data) != "new" {
		t.Fatalf("new theme not applied: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(layout.UserData(), "FontAndColorThemes", "README.txt")); err != nil {
		t.Fatalf("unmanaged file changed: %v", err)
	}
	if fake.preferences["DVTTextShowMinimap"].Value != true {
		t.Fatal("preference not applied")
	}

	restored, err := Rollback(context.Background(), layout, "1.0.0", "Xcode 26.6\nBuild version 17F113", fake.run)
	if err != nil {
		t.Fatal(err)
	}
	if restored != backup {
		t.Fatalf("restored %s, want %s", restored, backup)
	}
	if data, err := os.ReadFile(oldTheme); err != nil || string(data) != "old" {
		t.Fatalf("old theme not restored: %q, %v", data, err)
	}
	if fake.preferences["DVTTextShowMinimap"].Value != false {
		t.Fatal("preference not restored")
	}
}

func TestApplyAcceptsMissingDefaultsDomain(t *testing.T) {
	layout := testLayout(t)
	fake := newFakeDefaults(blankPreferences())
	fake.missingText = "Domain (com.apple.dt.Xcode) not found.\nDefaults have not been changed."
	content := Content{Preferences: blankPreferences(), Files: map[string]FileEntry{}}

	if err := ApplyContent(context.Background(), layout, content, fake.run); err != nil {
		t.Fatalf("missing defaults domain should be an absent preference: %v", err)
	}
}

func TestMissingDefaultDetectionIsDomainSpecific(t *testing.T) {
	err := errors.New("exit status 1")
	if !isMissingDefault([]byte("does not exist"), err) {
		t.Fatal("missing key was not detected")
	}
	if !isMissingDefault([]byte("Domain (com.apple.dt.Xcode) not found."), err) {
		t.Fatal("missing Xcode domain was not detected")
	}
	if isMissingDefault([]byte("Domain (com.example.Other) not found."), err) {
		t.Fatal("unrelated missing domain was accepted")
	}
}

func TestValidateContentRejectsUnmanagedAndUnsafeFiles(t *testing.T) {
	content := Content{Preferences: blankPreferences(), Files: map[string]FileEntry{
		filepath.Join("..", "secret.xccolortheme"): {Data: []byte("secret"), Mode: 0600},
	}}
	if err := ValidateContent(content); err == nil {
		t.Fatal("expected traversal path to fail")
	}
	content.Files = map[string]FileEntry{
		filepath.Join("FontAndColorThemes", "Night.xccolortheme"): {Data: []byte("theme"), Mode: 0666},
	}
	if err := ValidateContent(content); err == nil {
		t.Fatal("expected unsafe mode to fail")
	}
}

func TestCompareIsRedactedAndDeterministic(t *testing.T) {
	current := Content{Preferences: blankPreferences(), Files: map[string]FileEntry{}}
	wanted := Content{Preferences: blankPreferences(), Files: map[string]FileEntry{
		filepath.Join("FontAndColorThemes", "Night.xccolortheme"): {Data: []byte("private theme contents"), Mode: 0600},
	}}
	wanted.Preferences["XCFontAndColorCurrentDarkTheme"] = PreferenceEntry{Present: true, Value: "Night.xccolortheme"}
	changes := Compare(current, wanted)
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2", len(changes))
	}
	for _, change := range changes {
		if strings.Contains(change.Name, "private") {
			t.Fatal("change output contains a value")
		}
	}
}

func blankPreferences() map[string]PreferenceEntry {
	preferences := make(map[string]PreferenceEntry, len(preferenceSpecs))
	for name := range preferenceSpecs {
		preferences[name] = PreferenceEntry{Present: false}
	}
	return preferences
}

func testLayout(t *testing.T) Layout {
	t.Helper()
	root := t.TempDir()
	return Layout{
		Home: root, UserDataPath: filepath.Join(root, "UserData"), StatePath: filepath.Join(root, "state"),
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

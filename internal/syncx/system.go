package syncx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const preferencesDomain = "com.apple.dt.Xcode"

type CommandFunc func(context.Context, string, ...string) ([]byte, error)

func RunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return output, nil
}

func XcodeVersion(ctx context.Context, run CommandFunc) (string, error) {
	output, err := run(ctx, "xcodebuild", "-version")
	if err != nil {
		return "", fmt.Errorf("read Xcode version: %w", err)
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", fmt.Errorf("xcodebuild returned an empty version")
	}
	return version, nil
}

func XcodeRunning(ctx context.Context, run CommandFunc) (bool, error) {
	_, err := run(ctx, "pgrep", "-x", "Xcode")
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether Xcode is running: %w", err)
}

func BuildContent(ctx context.Context, layout Layout, run CommandFunc) (Content, error) {
	preferences, err := readPreferences(ctx, run)
	if err != nil {
		return Content{}, err
	}
	files, err := readManagedFiles(layout)
	if err != nil {
		return Content{}, err
	}
	content := Content{Preferences: preferences, Files: files}
	if err := ValidateContent(content); err != nil {
		return Content{}, err
	}
	return content, nil
}

func readPreferences(ctx context.Context, run CommandFunc) (map[string]PreferenceEntry, error) {
	preferences := make(map[string]PreferenceEntry, len(preferenceSpecs))
	for name, kind := range preferenceSpecs {
		typeOutput, err := run(ctx, "defaults", "read-type", preferencesDomain, name)
		if err != nil {
			if isMissingDefault(typeOutput, err) {
				preferences[name] = PreferenceEntry{Present: false}
				continue
			}
			return nil, fmt.Errorf("read preference type %s: %w", name, err)
		}
		expectedType := "Type is boolean"
		if kind == preferenceString {
			expectedType = "Type is string"
		}
		if strings.TrimSpace(string(typeOutput)) != expectedType {
			return nil, fmt.Errorf("preference %s has unsupported type %q", name, strings.TrimSpace(string(typeOutput)))
		}
		output, err := run(ctx, "defaults", "read", preferencesDomain, name)
		if err != nil {
			return nil, fmt.Errorf("read preference %s: %w", name, err)
		}
		text := strings.TrimSuffix(string(output), "\n")
		entry := PreferenceEntry{Present: true}
		switch kind {
		case preferenceBoolean:
			value, parseErr := strconv.ParseBool(text)
			if parseErr != nil {
				if text == "1" {
					value = true
				} else if text == "0" {
					value = false
				} else {
					return nil, fmt.Errorf("preference %s has invalid boolean value", name)
				}
			}
			entry.Value = value
		case preferenceString:
			entry.Value = text
		}
		preferences[name] = entry
	}
	return preferences, nil
}

func readManagedFiles(layout Layout) (map[string]FileEntry, error) {
	files := make(map[string]FileEntry)
	for _, managed := range managedDirectories {
		directory := filepath.Join(layout.UserData(), managed.Name)
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", directory, err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), managed.Extension) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, fmt.Errorf("inspect %s: %w", entry.Name(), err)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("managed file %s is not a regular file", filepath.Join(managed.Name, entry.Name()))
			}
			if info.Size() > maxFileSize {
				return nil, fmt.Errorf("managed file %s exceeds 1 MiB", filepath.Join(managed.Name, entry.Name()))
			}
			data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
			}
			relative := filepath.Join(managed.Name, entry.Name())
			files[relative] = FileEntry{Data: bytes.Clone(data), Mode: 0600}
		}
	}
	return files, nil
}

func isMissingDefault(output []byte, err error) bool {
	text := string(output) + " " + err.Error()
	return strings.Contains(text, "does not exist") ||
		strings.Contains(text, "Domain ("+preferencesDomain+") not found.")
}

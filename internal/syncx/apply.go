package syncx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func ApplyWithBackup(ctx context.Context, layout Layout, wanted Content, toolVersion, xcodeVersion string, run CommandFunc, now time.Time) (string, error) {
	current, err := BuildContent(ctx, layout, run)
	if err != nil {
		return "", fmt.Errorf("capture current settings: %w", err)
	}
	backup, err := NewBundle(current, toolVersion, xcodeVersion, "backup", now)
	if err != nil {
		return "", err
	}
	backupPath, err := writeBackup(layout, backup, now)
	if err != nil {
		return "", err
	}
	if err := ApplyContent(ctx, layout, wanted, run); err != nil {
		restoreErr := ApplyContent(context.WithoutCancel(ctx), layout, current, run)
		if restoreErr != nil {
			return backupPath, fmt.Errorf("apply settings: %v; restore backup: %w", err, restoreErr)
		}
		return backupPath, fmt.Errorf("apply settings: %w; original settings restored", err)
	}
	return backupPath, nil
}

func ApplyContent(ctx context.Context, layout Layout, content Content, run CommandFunc) error {
	if err := ValidateContent(content); err != nil {
		return err
	}
	if err := applyFiles(layout, content.Files); err != nil {
		return err
	}
	if err := applyPreferences(ctx, content.Preferences, run); err != nil {
		return err
	}
	return nil
}

func Rollback(ctx context.Context, layout Layout, toolVersion, xcodeVersion string, run CommandFunc) (string, error) {
	entries, err := os.ReadDir(layout.Backups())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no backups are available")
		}
		return "", fmt.Errorf("read backups: %w", err)
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join(layout.Backups(), entry.Name()))
		}
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("no backups are available")
	}
	sort.Strings(paths)
	path := paths[len(paths)-1]
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read backup: %w", err)
	}
	bundle, err := DecodeBundle(data)
	if err != nil {
		return "", err
	}
	if err := ValidateBundle(bundle, toolVersion, xcodeVersion); err != nil {
		return "", fmt.Errorf("validate backup: %w", err)
	}
	if bundle.Manifest.SourceRole != "backup" {
		return "", fmt.Errorf("latest backup has invalid role %q", bundle.Manifest.SourceRole)
	}
	if err := ApplyContent(ctx, layout, bundle.Content, run); err != nil {
		return "", fmt.Errorf("restore backup: %w", err)
	}
	return path, nil
}

func OperationLock(layout Layout) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(layout.LockFile()), 0700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	file, err := os.OpenFile(layout.LockFile(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open operation lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("another xcode-sync operation is running")
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func writeBackup(layout Layout, bundle Bundle, now time.Time) (string, error) {
	if err := os.MkdirAll(layout.Backups(), 0700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	data, err := EncodeBundle(bundle)
	if err != nil {
		return "", err
	}
	name := now.UTC().Format("20060102T150405.000000000Z") + ".json"
	path := filepath.Join(layout.Backups(), name)
	if err := writeAtomic(path, append(data, '\n'), 0600); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	return path, nil
}

func applyFiles(layout Layout, files map[string]FileEntry) error {
	for _, managed := range managedDirectories {
		directory := filepath.Join(layout.UserData(), managed.Name)
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create %s: %w", directory, err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return fmt.Errorf("read %s: %w", directory, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), managed.Extension) {
				relative := filepath.Join(managed.Name, entry.Name())
				if _, keep := files[relative]; !keep {
					if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
						return fmt.Errorf("remove %s: %w", relative, err)
					}
				}
			}
		}
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		file := files[relative]
		path := filepath.Join(layout.UserData(), relative)
		if err := writeAtomic(path, file.Data, os.FileMode(file.Mode)); err != nil {
			return fmt.Errorf("write %s: %w", relative, err)
		}
	}
	return nil
}

func applyPreferences(ctx context.Context, preferences map[string]PreferenceEntry, run CommandFunc) error {
	names := make([]string, 0, len(preferences))
	for name := range preferences {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := preferences[name]
		if !entry.Present {
			output, err := run(ctx, "defaults", "delete", preferencesDomain, name)
			if err != nil && !isMissingDefault(output, err) {
				return fmt.Errorf("delete preference %s: %w", name, err)
			}
			continue
		}
		kind := preferenceSpecs[name]
		var flag, value string
		if kind == preferenceBoolean {
			flag = "-bool"
			value = strconv.FormatBool(entry.Value.(bool))
		} else {
			flag = "-string"
			value = entry.Value.(string)
		}
		if _, err := run(ctx, "defaults", "write", preferencesDomain, name, flag, value); err != nil {
			return fmt.Errorf("write preference %s: %w", name, err)
		}
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".xcode-sync-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

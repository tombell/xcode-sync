package syncx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion = 1
	maxFileSize   = 1 << 20
	maxBundleSize = 4 << 20
)

type Layout struct {
	Home         string
	UserDataPath string
	StatePath    string
}

func LiveLayout(userDataPath, statePath string) (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, fmt.Errorf("find home directory: %w", err)
	}
	if statePath == "" {
		statePath = os.Getenv("XDG_STATE_HOME")
	}
	for name, value := range map[string]string{"user data path": userDataPath, "state path": statePath} {
		if value != "" && !filepath.IsAbs(value) {
			return Layout{}, fmt.Errorf("%s must be absolute", name)
		}
	}
	return Layout{Home: home, UserDataPath: filepath.Clean(userDataPath), StatePath: filepath.Clean(statePath)}, nil
}

func (layout Layout) UserData() string {
	if layout.UserDataPath != "" && layout.UserDataPath != "." {
		return layout.UserDataPath
	}
	return filepath.Join(layout.Home, "Library", "Developer", "Xcode", "UserData")
}

func (layout Layout) StateHome() string {
	if layout.StatePath != "" && layout.StatePath != "." {
		return layout.StatePath
	}
	return filepath.Join(layout.Home, ".local", "state")
}

func (layout Layout) Backups() string {
	return filepath.Join(layout.StateHome(), "xcode-sync", "backups")
}

func (layout Layout) LockFile() string {
	return filepath.Join(layout.StateHome(), "xcode-sync", "operation.lock")
}

type PreferenceEntry struct {
	Present bool `json:"present"`
	Value   any  `json:"value,omitempty"`
}

type FileEntry struct {
	Data []byte `json:"data"`
	Mode uint32 `json:"mode"`
}

type Content struct {
	Preferences map[string]PreferenceEntry `json:"preferences"`
	Files       map[string]FileEntry       `json:"files"`
}

type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`
	XcodeVersion  string `json:"xcode_version"`
	SourceRole    string `json:"source_role"`
	ExportedAt    string `json:"exported_at"`
	ContentSHA256 string `json:"content_sha256"`
}

type Bundle struct {
	Manifest Manifest `json:"manifest"`
	Content  Content  `json:"content"`
}

type Change struct {
	Kind string
	Name string
}

type preferenceKind int

const (
	preferenceBoolean preferenceKind = iota
	preferenceString
)

var preferenceSpecs = map[string]preferenceKind{
	"DVTTextEditorTrimWhitespaceOnlyLines":              preferenceBoolean,
	"DVTTextEnablePredictiveCompletion":                 preferenceBoolean,
	"DVTTextShowMinimap":                                preferenceBoolean,
	"IDEKeyBindingCurrentPreferenceSet":                 preferenceString,
	"IDEStringCatalogAutomaticCommentGenerationEnabled": preferenceBoolean,
	"IDEWorkspaceSuppressCleanBuildPrompt":              preferenceBoolean,
	"XCFontAndColorCurrentDarkTheme":                    preferenceString,
	"XCFontAndColorCurrentLightTheme":                   preferenceString,
	"XCFontAndColorCurrentTheme":                        preferenceString,
}

type managedDirectory struct {
	Name      string
	Extension string
}

var managedDirectories = []managedDirectory{
	{Name: "FontAndColorThemes", Extension: ".xccolortheme"},
	{Name: "KeyBindings", Extension: ".idekeybindings"},
	{Name: "CodeSnippets", Extension: ".codesnippet"},
}

func NewBundle(content Content, toolVersion, xcodeVersion, sourceRole string, now time.Time) (Bundle, error) {
	hash, err := contentHash(content)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		Manifest: Manifest{
			SchemaVersion: SchemaVersion,
			ToolVersion:   toolVersion,
			XcodeVersion:  xcodeVersion,
			SourceRole:    sourceRole,
			ExportedAt:    now.UTC().Format(time.RFC3339),
			ContentSHA256: hash,
		},
		Content: content,
	}, nil
}

func EncodeBundle(bundle Bundle) ([]byte, error) {
	return json.MarshalIndent(bundle, "", "  ")
}

func DecodeBundle(data []byte) (Bundle, error) {
	var bundle Bundle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return Bundle{}, fmt.Errorf("decode bundle: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Bundle{}, fmt.Errorf("decode bundle: trailing JSON value")
	}
	return bundle, nil
}

func ValidateBundle(bundle Bundle, toolVersion, xcodeVersion string) error {
	if bundle.Manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("bundle schema %d is unsupported", bundle.Manifest.SchemaVersion)
	}
	if bundle.Manifest.SourceRole != "source" && bundle.Manifest.SourceRole != "backup" {
		return fmt.Errorf("bundle has invalid source role %q", bundle.Manifest.SourceRole)
	}
	if bundle.Manifest.ToolVersion != toolVersion {
		return fmt.Errorf("source uses xcode-sync %s; local version is %s", bundle.Manifest.ToolVersion, toolVersion)
	}
	if bundle.Manifest.XcodeVersion != xcodeVersion {
		return fmt.Errorf("source uses %q; local Xcode is %q", bundle.Manifest.XcodeVersion, xcodeVersion)
	}
	if _, err := time.Parse(time.RFC3339, bundle.Manifest.ExportedAt); err != nil {
		return fmt.Errorf("bundle has invalid export time")
	}
	if err := ValidateContent(bundle.Content); err != nil {
		return err
	}
	hash, err := contentHash(bundle.Content)
	if err != nil {
		return err
	}
	if hash != bundle.Manifest.ContentSHA256 {
		return fmt.Errorf("bundle content hash does not match")
	}
	return nil
}

func ValidateContent(content Content) error {
	if len(content.Preferences) != len(preferenceSpecs) {
		return fmt.Errorf("bundle preference set is incomplete or contains unknown keys")
	}
	for name, kind := range preferenceSpecs {
		entry, ok := content.Preferences[name]
		if !ok {
			return fmt.Errorf("bundle is missing preference %s", name)
		}
		if !entry.Present {
			if entry.Value != nil {
				return fmt.Errorf("absent preference %s contains a value", name)
			}
			continue
		}
		switch kind {
		case preferenceBoolean:
			if _, ok := entry.Value.(bool); !ok {
				return fmt.Errorf("preference %s must be boolean", name)
			}
		case preferenceString:
			value, ok := entry.Value.(string)
			if !ok || value == "" || strings.ContainsAny(value, "\x00\r\n") {
				return fmt.Errorf("preference %s must be a non-empty single-line string", name)
			}
		}
	}
	total := 0
	for path, file := range content.Files {
		if !validManagedPath(path) {
			return fmt.Errorf("bundle contains unmanaged path %q", path)
		}
		if len(file.Data) > maxFileSize {
			return fmt.Errorf("file %s exceeds 1 MiB", path)
		}
		if file.Mode != 0600 {
			return fmt.Errorf("file %s has unsafe permissions", path)
		}
		total += len(file.Data)
	}
	if total > maxBundleSize {
		return fmt.Errorf("managed files exceed 4 MiB")
	}
	return nil
}

func Compare(current, wanted Content) []Change {
	changes := make([]Change, 0)
	for name := range preferenceSpecs {
		left, right := current.Preferences[name], wanted.Preferences[name]
		if left.Present != right.Present || !valuesEqual(left.Value, right.Value) {
			changes = append(changes, Change{Kind: "preference", Name: name})
		}
	}
	paths := make(map[string]struct{}, len(current.Files)+len(wanted.Files))
	for path := range current.Files {
		paths[path] = struct{}{}
	}
	for path := range wanted.Files {
		paths[path] = struct{}{}
	}
	for path := range paths {
		left, leftOK := current.Files[path]
		right, rightOK := wanted.Files[path]
		if leftOK != rightOK || left.Mode != right.Mode || sha256Bytes(left.Data) != sha256Bytes(right.Data) {
			changes = append(changes, Change{Kind: "file", Name: path})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Kind == changes[j].Kind {
			return changes[i].Name < changes[j].Name
		}
		return changes[i].Kind < changes[j].Kind
	})
	return changes
}

func contentHash(content Content) (string, error) {
	data, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("encode bundle content: %w", err)
	}
	return sha256Bytes(data), nil
}

func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func valuesEqual(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func validManagedPath(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.Contains(path, "\\") {
		return false
	}
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) != 2 || parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return false
	}
	for _, directory := range managedDirectories {
		if parts[0] == directory.Name && strings.HasSuffix(parts[1], directory.Extension) {
			return true
		}
	}
	return false
}

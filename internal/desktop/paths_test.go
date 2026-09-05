package desktop

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Hans2573/OpenCode-Handoff/internal/config"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
)

func TestBootstrapPathsImportsLegacyFilesWithoutOverwriting(t *testing.T) {
	legacy := t.TempDir()
	localData := t.TempDir()
	t.Setenv("AGENT_HANDOFF_DATA_DIR", localData)
	if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("logging:\n  level: debug\nfeishu:\n  app_secret: ${KEEP_SECRET}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(context.Background(), filepath.Join(legacy, "opencode-handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "persisted", "yes"); err != nil {
		t.Fatal(err)
	}

	paths, err := BootstrapPaths(legacy)
	if err != nil {
		t.Fatal(err)
	}
	configData, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configData), "${KEEP_SECRET}") || !strings.Contains(string(configData), "debug") {
		t.Fatalf("config=%q", configData)
	}
	imported, err := store.OpenSQLite(context.Background(), paths.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	value, err := imported.GetSetting(context.Background(), "persisted")
	imported.Close()
	if err != nil || value != "yes" {
		t.Fatalf("imported setting = %q, %v", value, err)
	}
	if err := os.WriteFile(paths.ConfigPath, []byte("desktop-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BootstrapPaths(legacy); err != nil {
		t.Fatal(err)
	}
	configData, _ = os.ReadFile(paths.ConfigPath)
	if string(configData) != "desktop-config" {
		t.Fatalf("second bootstrap overwrote config: %q", configData)
	}
}

func TestWindowsPathsDoNotDependOnAppDataOrWorkingDirectory(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows profile location")
	}
	t.Setenv("AGENT_HANDOFF_DATA_DIR", "")
	profile := t.TempDir()
	t.Setenv("USERPROFILE", profile)
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "normal"))
	first, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "Packages", "launcher", "LocalCache", "Local"))
	t.Chdir(t.TempDir())
	second, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.DataDir != filepath.Join(profile, ".agent-handoff") {
		t.Fatalf("paths changed: %+v -> %+v", first, second)
	}
}

func TestBootstrapRejectsAmbiguousLegacyDatabases(t *testing.T) {
	t.Setenv("AGENT_HANDOFF_DATA_DIR", t.TempDir())
	dirs := []string{t.TempDir(), t.TempDir()}
	for _, dir := range dirs {
		db, err := store.OpenSQLite(context.Background(), filepath.Join(dir, "opencode-handoff.db"))
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	paths, _ := DefaultPaths()
	if _, err := BootstrapPaths(dirs...); err == nil {
		t.Fatal("silently selected one of two databases")
	}
	if fileExists(paths.ConfigPath) || fileExists(paths.StorePath) {
		t.Fatal("ambiguous import published a new data set")
	}
}

func TestBootstrapRebasesCustomDatabaseAndKeepsConfigWithItsStore(t *testing.T) {
	t.Setenv("AGENT_HANDOFF_DATA_DIR", t.TempDir())
	cli, desktop := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cli, "config.yaml"), []byte("logging:\n  level: error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(desktop, "config.yaml"), []byte("store:\n  path: custom.db\nlogging:\n  level: debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(context.Background(), filepath.Join(desktop, "custom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "source", "desktop"); err != nil {
		t.Fatal(err)
	}
	paths, err := BootstrapPaths(cli, desktop)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadUnvalidated(paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.Level != "debug" || cfg.Store.Path != paths.StorePath {
		t.Fatalf("wrong config/store pair: level=%s, path=%s", cfg.Logging.Level, cfg.Store.Path)
	}
}

func TestBootstrapDoesNotPublishCorruptLegacyDatabase(t *testing.T) {
	t.Setenv("AGENT_HANDOFF_DATA_DIR", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode-handoff.db"), []byte("broken database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BootstrapPaths(dir); err == nil {
		t.Fatal("imported corrupt database")
	}
	paths, _ := DefaultPaths()
	if fileExists(paths.StorePath) || fileExists(paths.ConfigPath) {
		t.Fatal("failed import left published files")
	}
}

func TestFileSizesReportsExistingFilesAndTreatsMissingFilesAsEmpty(t *testing.T) {
	directory := t.TempDir()
	paths := Paths{
		ConfigPath: filepath.Join(directory, "config.yaml"),
		StorePath:  filepath.Join(directory, "opencode-handoff.db"),
		LogPath:    filepath.Join(directory, "missing.log"),
	}
	if err := os.WriteFile(paths.ConfigPath, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.StorePath, []byte("123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	sizes := fileSizes(paths)
	if sizes.Config != 5 || sizes.Store != 9 || sizes.Log != 0 {
		t.Fatalf("unexpected file sizes: %+v", sizes)
	}
}

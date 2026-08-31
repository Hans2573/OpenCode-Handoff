package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapPathsImportsLegacyFilesWithoutOverwriting(t *testing.T) {
	legacy := t.TempDir()
	localData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localData)
	if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("legacy-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "opencode-handoff.db"), []byte("legacy-db"), 0o600); err != nil {
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
	if string(configData) != "legacy-config" {
		t.Fatalf("config=%q", configData)
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

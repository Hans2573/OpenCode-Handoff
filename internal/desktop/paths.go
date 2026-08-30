package desktop

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Hans2573/OpenCode-Handoff/internal/config"
)

const dataDirectoryName = "Agent Handoff"

type Paths struct {
	DataDir    string `json:"dataDir"`
	ConfigPath string `json:"configPath"`
	StorePath  string `json:"storePath"`
	LogPath    string `json:"logPath"`
}

func DefaultPaths() (Paths, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve local application data directory: %w", err)
		}
	}
	dataDir := filepath.Join(root, dataDirectoryName)
	return Paths{
		DataDir:    dataDir,
		ConfigPath: filepath.Join(dataDir, "config.yaml"),
		// Keep the historical filename so an imported relative store.path keeps
		// pointing at the copied database.
		StorePath: filepath.Join(dataDir, "opencode-handoff.db"),
		LogPath:   filepath.Join(dataDir, "logs", "agent-handoff.log"),
	}, nil
}

// BootstrapPaths creates the per-user desktop data directory and, on first
// launch, imports the current working directory's CLI configuration and SQLite
// database. Existing desktop files are never overwritten.
func BootstrapPaths(legacyDirs ...string) (Paths, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(filepath.Join(paths.DataDir, "logs"), 0o700); err != nil {
		return Paths{}, fmt.Errorf("create desktop data directory: %w", err)
	}

	legacyConfig := firstExistingFile(legacyDirs, "config.yaml")
	legacyStore := firstExistingFile(legacyDirs, "opencode-handoff.db")
	if !fileExists(paths.ConfigPath) {
		if legacyConfig != "" {
			if err := copyFile(legacyConfig, paths.ConfigPath, 0o600); err != nil {
				return Paths{}, fmt.Errorf("import legacy configuration: %w", err)
			}
			_ = copyFile(legacyConfig, paths.ConfigPath+".imported.bak", 0o600)
		} else {
			cfg := config.Default()
			cfg.Store.Path = paths.StorePath
			if err := config.Save(paths.ConfigPath, cfg); err != nil {
				return Paths{}, err
			}
		}
	}
	if !fileExists(paths.StorePath) && legacyStore != "" {
		if err := copyFile(legacyStore, paths.StorePath, 0o600); err != nil {
			return Paths{}, fmt.Errorf("import legacy database: %w", err)
		}
		_ = copyFile(legacyStore, paths.StorePath+".imported.bak", 0o600)
	}
	return paths, nil
}

func firstExistingFile(directories []string, name string) string {
	seen := make(map[string]struct{})
	for _, directory := range directories {
		directory = filepath.Clean(strings.TrimSpace(directory))
		if directory == "." || directory == "" {
			continue
		}
		key := strings.ToLower(directory)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		candidate := filepath.Join(directory, name)
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func copyFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	succeeded := false
	defer func() {
		_ = output.Close()
		if !succeeded {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	succeeded = true
	return nil
}

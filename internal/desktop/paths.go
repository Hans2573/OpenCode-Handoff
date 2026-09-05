package desktop

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Hans2573/OpenCode-Handoff/internal/config"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
	"gopkg.in/yaml.v3"
)

const dataDirectoryName = "Agent Handoff"

type Paths struct {
	DataDir    string `json:"dataDir"`
	ConfigPath string `json:"configPath"`
	StorePath  string `json:"storePath"`
	LogPath    string `json:"logPath"`
}

func DefaultPaths() (Paths, error) {
	dataDir := strings.TrimSpace(os.Getenv("AGENT_HANDOFF_DATA_DIR"))
	if dataDir != "" && !filepath.IsAbs(dataDir) {
		return Paths{}, fmt.Errorf("AGENT_HANDOFF_DATA_DIR must be an absolute path")
	}
	if dataDir == "" && runtime.GOOS == "windows" {
		// AppData is virtualized when launched by an MSIX-packaged parent.
		// A profile-level directory is shared by packaged and ordinary shells.
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve user profile: %w", err)
		}
		dataDir = filepath.Join(home, ".agent-handoff")
	}
	if dataDir == "" {
		root := os.Getenv("LOCALAPPDATA")
		if root == "" {
			var err error
			root, err = os.UserConfigDir()
			if err != nil {
				return Paths{}, fmt.Errorf("resolve local application data directory: %w", err)
			}
		}
		dataDir = filepath.Join(root, dataDirectoryName)
	}
	dataDir = filepath.Clean(dataDir)
	return Paths{
		DataDir:    dataDir,
		ConfigPath: filepath.Join(dataDir, "config.yaml"),
		// Keep the historical filename so an imported relative store.path keeps
		// pointing at the copied database.
		StorePath: filepath.Join(dataDir, "opencode-handoff.db"),
		LogPath:   filepath.Join(dataDir, "logs", "agent-handoff.log"),
	}, nil
}

// BootstrapPaths imports one coherent legacy configuration/database pair.
// Existing desktop files are never overwritten; ambiguous legacy databases
// require explicit recovery instead of silently choosing an empty copy.
func BootstrapPaths(legacyDirs ...string) (Paths, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(filepath.Join(paths.DataDir, "logs"), 0o700); err != nil {
		return Paths{}, fmt.Errorf("create desktop data directory: %w", err)
	}

	if fileExists(paths.ConfigPath) {
		return paths, nil
	}
	if os.Getenv("AGENT_HANDOFF_DATA_DIR") == "" && runtime.GOOS == "windows" {
		oldDirs := legacyDesktopDirectories(os.Getenv("LOCALAPPDATA"))
		if firstExistingFile(oldDirs, "config.yaml") != "" || firstExistingFile(oldDirs, "opencode-handoff.db") != "" {
			legacyDirs = oldDirs
		}
	}
	legacyConfig, legacyStore, err := selectLegacyState(legacyDirs)
	if err != nil {
		return Paths{}, err
	}
	if !fileExists(paths.StorePath) && legacyStore != "" {
		if err := store.SnapshotSQLite(context.Background(), legacyStore, paths.StorePath); err != nil {
			return Paths{}, fmt.Errorf("import legacy database: %w", err)
		}
		_ = copyFile(paths.StorePath, paths.StorePath+".imported.bak", 0o600)
	}
	if legacyConfig != "" {
		if err := importConfig(legacyConfig, paths); err != nil {
			return Paths{}, err
		}
		_ = copyFile(legacyConfig, paths.ConfigPath+".imported.bak", 0o600)
	} else {
		cfg := config.Default()
		cfg.Store.Path = paths.StorePath
		if err := config.Save(paths.ConfigPath, cfg); err != nil {
			return Paths{}, err
		}
	}
	return paths, nil
}

func legacyDesktopDirectories(localData string) []string {
	if localData == "" {
		return nil
	}
	dirs := []string{filepath.Join(localData, dataDirectoryName), filepath.Join(localData, "OpenCode Handoff")}
	for _, name := range []string{dataDirectoryName, "OpenCode Handoff"} {
		packaged, _ := filepath.Glob(filepath.Join(localData, "Packages", "*", "LocalCache", "Local", name))
		dirs = append(dirs, packaged...)
	}
	return dirs
}

func selectLegacyState(dirs []string) (string, string, error) {
	selectedConfig := firstExistingFile(dirs, "config.yaml")
	var selectedStore string
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		configPath := filepath.Join(dir, "config.yaml")
		storePath := filepath.Join(dir, "opencode-handoff.db")
		if fileExists(configPath) {
			data, err := os.ReadFile(configPath)
			if err != nil {
				return "", "", err
			}
			var cfg struct {
				Store config.StoreConfig `yaml:"store"`
			}
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return "", "", fmt.Errorf("read legacy config %s: %w", configPath, err)
			}
			if cfg.Store.Path != "" {
				missing := false
				storePath = os.Expand(cfg.Store.Path, func(key string) string {
					value, ok := os.LookupEnv(key)
					missing = missing || !ok
					return value
				})
				if missing {
					return "", "", fmt.Errorf("legacy store.path in %s references an unset environment variable", configPath)
				}
				if storePath == ":memory:" || strings.HasPrefix(storePath, "file:") {
					return "", "", fmt.Errorf("legacy store.path in %s must name a persistent database file", configPath)
				}
				if !filepath.IsAbs(storePath) {
					storePath = filepath.Join(dir, storePath)
				}
			}
		}
		info, err := os.Stat(storePath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if info.IsDir() {
			return "", "", fmt.Errorf("legacy database is a directory: %s", storePath)
		}
		if selectedStore != "" {
			previous, err := os.Stat(selectedStore)
			if err != nil {
				return "", "", err
			}
			if os.SameFile(previous, info) {
				continue
			}
			return "", "", fmt.Errorf("发现多个旧数据库，请先备份并合并到新的数据目录，避免读取错误的数据：%s；%s", selectedStore, storePath)
		}
		selectedStore = storePath
		selectedConfig = ""
		if fileExists(configPath) {
			selectedConfig = configPath
		}
	}
	return selectedConfig, selectedStore, nil
}

func importConfig(source string, paths Paths) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("legacy configuration must be a YAML mapping: %s", source)
	}
	root := doc.Content[0]
	var storeNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "store" {
			storeNode = root.Content[i+1]
			break
		}
	}
	if storeNode == nil {
		storeNode = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "store"}, storeNode)
	}
	// Only rebase the database path. Keep secrets and ${ENV} expressions verbatim.
	*storeNode = yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "path"}, {Kind: yaml.ScalarNode, Tag: "!!str", Value: paths.StorePath},
	}}
	encoded, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(paths.ConfigPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	succeeded := false
	defer func() {
		_ = file.Close()
		if !succeeded {
			_ = os.Remove(paths.ConfigPath)
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	succeeded = true
	return nil
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

func fileSizes(paths Paths) FileSizes {
	return FileSizes{
		Config: fileSize(paths.ConfigPath),
		Store:  fileSize(paths.StorePath),
		Log:    fileSize(paths.LogPath),
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
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

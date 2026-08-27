package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExpandsEnvironmentAndAppliesDefaults(t *testing.T) {
	clearAutomaticOverrides(t)
	t.Setenv("TEST_APP_ID", "cli_test")
	t.Setenv("TEST_SECRET", "secret")
	t.Setenv("TEST_CHAT", "oc_test")
	t.Setenv("TEST_USER", "ou_test")

	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	content := `
feishu:
  app_id: ${TEST_APP_ID}
  app_secret: ${TEST_SECRET}
  chat_id: ${TEST_CHAT}
security:
  allowed_users: [${TEST_USER}]
store:
  path: state/handoff.db
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OpenCode.BaseURL != "http://127.0.0.1:4096" {
		t.Fatalf("unexpected default base URL: %s", cfg.OpenCode.BaseURL)
	}
	if cfg.Watcher.PollingInterval.Duration != 3*time.Second {
		t.Fatalf("unexpected polling interval: %s", cfg.Watcher.PollingInterval.Duration)
	}
	wantStore := filepath.Join(directory, "state", "handoff.db")
	if cfg.Store.Path != wantStore {
		t.Fatalf("store path = %q, want %q", cfg.Store.Path, wantStore)
	}
}

func TestValidateRejectsRemoteOpenCodeByDefault(t *testing.T) {
	cfg := validConfig()
	cfg.OpenCode.BaseURL = "http://192.0.2.10:4096"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Validate() error = %v, want loopback error", err)
	}
	cfg.OpenCode.AllowRemote = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with allow_remote error = %v", err)
	}
}

func TestValidateAllowsPairingWithoutManualRoute(t *testing.T) {
	cfg := validConfig()
	cfg.Feishu.ChatID = ""
	cfg.Security.AllowedUsers = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEnvironmentOverridesLiteralYAML(t *testing.T) {
	t.Setenv("FEISHU_APP_ID", "cli_env")
	t.Setenv("FEISHU_APP_SECRET", "secret_env")
	t.Setenv("FEISHU_CHAT_ID", "oc_env")
	t.Setenv("FEISHU_ALLOWED_USERS", "ou_one, user_two")
	t.Setenv("OPENCODE_BASE_URL", "http://localhost:5000")
	t.Setenv("OPENCODE_DIRECTORY", "/work/env")
	t.Setenv("OPENCODE_SERVER_USERNAME", "env_user")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "env_password")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
opencode:
  base_url: http://127.0.0.1:4096
  directory: /work/yaml
  username: yaml_user
  password: yaml_password
feishu:
  app_id: cli_yaml
  app_secret: secret_yaml
  chat_id: oc_yaml
security:
  allowed_users: [ou_yaml]
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Feishu.AppID != "cli_env" || cfg.Feishu.AppSecret != "secret_env" || cfg.Feishu.ChatID != "oc_env" {
		t.Fatalf("Feishu env overrides not applied: %+v", cfg.Feishu)
	}
	if cfg.OpenCode.BaseURL != "http://localhost:5000" || cfg.OpenCode.Directory != "/work/env" || cfg.OpenCode.Username != "env_user" || cfg.OpenCode.Password != "env_password" {
		t.Fatalf("OpenCode env overrides not applied: %+v", cfg.OpenCode)
	}
	if len(cfg.Security.AllowedUsers) != 2 || cfg.Security.AllowedUsers[1] != "user_two" {
		t.Fatalf("allowed users = %v", cfg.Security.AllowedUsers)
	}
}

func validConfig() Config {
	cfg := Default()
	cfg.Feishu = FeishuConfig{AppID: "cli_test", AppSecret: "secret", ChatID: "oc_test"}
	cfg.Security.AllowedUsers = []string{"ou_test"}
	return cfg
}

func clearAutomaticOverrides(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OPENCODE_BASE_URL",
		"OPENCODE_DIRECTORY",
		"OPENCODE_SERVER_USERNAME",
		"OPENCODE_SERVER_PASSWORD",
		"FEISHU_APP_ID",
		"FEISHU_APP_SECRET",
		"FEISHU_CHAT_ID",
		"FEISHU_ALLOWED_USERS",
		"FEISHU_ALLOWED_USER",
	} {
		value, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

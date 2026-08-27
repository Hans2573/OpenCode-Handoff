package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	d.Duration = value
	return nil
}

type Config struct {
	OpenCode OpenCodeConfig `yaml:"opencode"`
	Watcher  WatcherConfig  `yaml:"watcher"`
	Handoff  HandoffConfig  `yaml:"handoff"`
	Channel  ChannelConfig  `yaml:"channel"`
	Feishu   FeishuConfig   `yaml:"feishu"`
	Security SecurityConfig `yaml:"security"`
	Store    StoreConfig    `yaml:"store"`
	Logging  LoggingConfig  `yaml:"logging"`
}

type OpenCodeConfig struct {
	BaseURL     string `yaml:"base_url"`
	Directory   string `yaml:"directory"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	AllowRemote bool   `yaml:"allow_remote"`
}

type WatcherConfig struct {
	SSE             bool     `yaml:"sse"`
	PollingFallback bool     `yaml:"polling_fallback"`
	PollingInterval Duration `yaml:"polling_interval"`
}

type HandoffConfig struct {
	MaxOutputChars   int  `yaml:"max_output_chars"`
	NotifyIdle       bool `yaml:"notify_idle"`
	NotifyError      bool `yaml:"notify_error"`
	NotifyQuestion   bool `yaml:"notify_question"`
	NotifyPermission bool `yaml:"notify_permission"`
}

type ChannelConfig struct {
	Type string `yaml:"type"`
}

type FeishuConfig struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
	ChatID    string `yaml:"chat_id"`
}

type SecurityConfig struct {
	AllowedUsers []string `yaml:"allowed_users"`
}

type StoreConfig struct {
	Path string `yaml:"path"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
}

func Default() Config {
	return Config{
		OpenCode: OpenCodeConfig{BaseURL: "http://127.0.0.1:4096", Username: "opencode"},
		Watcher: WatcherConfig{
			SSE:             true,
			PollingFallback: true,
			PollingInterval: Duration{Duration: 3 * time.Second},
		},
		Handoff: HandoffConfig{
			MaxOutputChars:   3000,
			NotifyIdle:       true,
			NotifyError:      true,
			NotifyQuestion:   true,
			NotifyPermission: true,
		},
		Channel: ChannelConfig{Type: "feishu"},
		Store:   StoreConfig{Path: "opencode-handoff.db"},
		Logging: LoggingConfig{Level: "info"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	expanded, err := expandEnvironment(string(data))
	if err != nil {
		return Config{}, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := applyEnvironmentOverrides(&cfg); err != nil {
		return Config{}, err
	}

	if cfg.Store.Path != ":memory:" && !filepath.IsAbs(cfg.Store.Path) {
		cfg.Store.Path = filepath.Join(filepath.Dir(path), cfg.Store.Path)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Environment variables override literal YAML values. YAML ${VAR} expansion is
// still supported for deployments that prefer explicitly selecting each source.
func applyEnvironmentOverrides(cfg *Config) error {
	overrideString("OPENCODE_BASE_URL", &cfg.OpenCode.BaseURL)
	overrideString("OPENCODE_DIRECTORY", &cfg.OpenCode.Directory)
	overrideString("OPENCODE_SERVER_USERNAME", &cfg.OpenCode.Username)
	overrideString("OPENCODE_SERVER_PASSWORD", &cfg.OpenCode.Password)
	overrideString("FEISHU_APP_ID", &cfg.Feishu.AppID)
	overrideString("FEISHU_APP_SECRET", &cfg.Feishu.AppSecret)
	overrideString("FEISHU_CHAT_ID", &cfg.Feishu.ChatID)

	if value, ok := os.LookupEnv("FEISHU_ALLOWED_USERS"); ok {
		cfg.Security.AllowedUsers = splitCommaSeparated(value)
	} else if value, ok := os.LookupEnv("FEISHU_ALLOWED_USER"); ok {
		cfg.Security.AllowedUsers = splitCommaSeparated(value)
	}
	if err := overrideInt("HANDOFF_MAX_OUTPUT_CHARS", &cfg.Handoff.MaxOutputChars); err != nil {
		return err
	}
	for name, target := range map[string]*bool{
		"HANDOFF_NOTIFY_IDLE":       &cfg.Handoff.NotifyIdle,
		"HANDOFF_NOTIFY_ERROR":      &cfg.Handoff.NotifyError,
		"HANDOFF_NOTIFY_QUESTION":   &cfg.Handoff.NotifyQuestion,
		"HANDOFF_NOTIFY_PERMISSION": &cfg.Handoff.NotifyPermission,
	} {
		if err := overrideBool(name, target); err != nil {
			return err
		}
	}
	return nil
}

func overrideString(name string, target *string) {
	if value, ok := os.LookupEnv(name); ok {
		*target = value
	}
}

func overrideBool(name string, target *bool) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("environment variable %s must be true or false: %w", name, err)
	}
	*target = parsed
	return nil
}

func overrideInt(name string, target *int) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("environment variable %s must be an integer: %w", name, err)
	}
	*target = parsed
	return nil
}

func splitCommaSeparated(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func (c Config) Validate() error {
	parsed, err := url.Parse(c.OpenCode.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return fmt.Errorf("opencode.base_url must be a valid absolute URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("opencode.base_url scheme must be http or https")
	}
	if !c.OpenCode.AllowRemote && !isLoopback(parsed.Hostname()) {
		return fmt.Errorf("opencode.base_url must use a loopback host unless opencode.allow_remote is true")
	}
	if !c.Watcher.SSE && !c.Watcher.PollingFallback {
		return errors.New("at least one watcher mode must be enabled")
	}
	if c.Watcher.PollingFallback && c.Watcher.PollingInterval.Duration <= 0 {
		return errors.New("watcher.polling_interval must be positive")
	}
	if c.Handoff.MaxOutputChars <= 0 {
		return errors.New("handoff.max_output_chars must be positive")
	}
	if c.Channel.Type != "feishu" {
		return fmt.Errorf("unsupported channel.type %q", c.Channel.Type)
	}
	if c.Feishu.AppID == "" || c.Feishu.AppSecret == "" {
		return errors.New("feishu.app_id and feishu.app_secret are required")
	}
	if c.Store.Path == "" {
		return errors.New("store.path is required")
	}
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unsupported logging.level %q", c.Logging.Level)
	}
	return nil
}

func expandEnvironment(input string) (string, error) {
	var missing []string
	expanded := os.Expand(input, func(key string) string {
		value, ok := os.LookupEnv(key)
		if !ok {
			missing = append(missing, key)
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return expanded, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

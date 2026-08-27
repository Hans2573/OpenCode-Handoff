package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/xiaohang2/opencode-handoff/internal/channel/feishu"
	"github.com/xiaohang2/opencode-handoff/internal/config"
	"github.com/xiaohang2/opencode-handoff/internal/handoff"
	"github.com/xiaohang2/opencode-handoff/internal/opencode"
	"github.com/xiaohang2/opencode-handoff/internal/store"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML configuration file")
	check := flag.Bool("check", false, "validate configuration and exit without connecting to services")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}
	if *check {
		fmt.Println("configuration is valid")
		return
	}

	logger := newLogger(cfg.Logging.Level)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	handoffStore, err := store.OpenSQLite(ctx, cfg.Store.Path)
	if err != nil {
		logger.Error("open handoff store", "error", err)
		os.Exit(1)
	}
	defer handoffStore.Close()
	pairingCode, err := preparePairing(ctx, cfg, handoffStore, logger)
	if err != nil {
		logger.Error("prepare Feishu pairing", "error", err)
		os.Exit(1)
	}

	opencodeClient, err := opencode.NewClient(opencode.ClientOptions{
		BaseURL:   cfg.OpenCode.BaseURL,
		Directory: cfg.OpenCode.Directory,
		Username:  cfg.OpenCode.Username,
		Password:  cfg.OpenCode.Password,
	})
	if err != nil {
		logger.Error("create OpenCode client", "error", err)
		os.Exit(1)
	}
	channelClient := feishu.New(feishu.Options{
		AppID:          cfg.Feishu.AppID,
		AppSecret:      cfg.Feishu.AppSecret,
		ChatID:         cfg.Feishu.ChatID,
		PairingCode:    pairingCode,
		AllowedUsers:   cfg.Security.AllowedUsers,
		BindingStore:   handoffStore,
		MaxOutputChars: cfg.Handoff.MaxOutputChars,
	}, logger)

	watcher := handoff.NewWatcher(opencodeClient, handoff.WatcherOptions{
		SSE:             cfg.Watcher.SSE,
		PollingFallback: cfg.Watcher.PollingFallback,
		PollingInterval: cfg.Watcher.PollingInterval.Duration,
		NotifyQuestions: cfg.Handoff.NotifyQuestion,
	}, logger)
	engine := handoff.NewEngine(opencodeClient, channelClient, handoffStore, handoff.EngineOptions{
		MaxOutputChars: cfg.Handoff.MaxOutputChars,
		NotifyIdle:     cfg.Handoff.NotifyIdle,
		NotifyError:    cfg.Handoff.NotifyError,
		NotifyQuestion: cfg.Handoff.NotifyQuestion,
		AllowedUsers:   cfg.Security.AllowedUsers,
		ChatID:         cfg.Feishu.ChatID,
	}, logger)

	logger.Info("OpenCode Handoff starting", "version", version, "opencode", cfg.OpenCode.BaseURL)
	if err := engine.Run(ctx, watcher.Run(ctx)); err != nil && ctx.Err() == nil {
		logger.Error("OpenCode Handoff stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("OpenCode Handoff stopped")
}

func preparePairing(ctx context.Context, cfg config.Config, handoffStore store.Store, logger *slog.Logger) (string, error) {
	binding, err := handoffStore.GetChannelBinding(ctx)
	if err == nil {
		if cfg.Feishu.ChatID != "" && cfg.Feishu.ChatID != binding.ChatID {
			return "", fmt.Errorf("configured feishu.chat_id %s differs from stored binding %s", cfg.Feishu.ChatID, binding.ChatID)
		}
		logger.Info("using stored Feishu pairing", "chat_id", binding.ChatID)
		return "", nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if cfg.Feishu.ChatID != "" && len(cfg.Security.AllowedUsers) > 0 {
		return "", nil
	}
	code, err := newPairingCode()
	if err != nil {
		return "", err
	}
	logger.Info("Feishu is not paired; send this command to the bot", "command", "/bind "+code)
	return code, nil
}

func newPairingCode() (string, error) {
	buffer := make([]byte, 5)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate pairing code: %w", err)
	}
	return strings.ToUpper(hex.EncodeToString(buffer)), nil
}

func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel}))
}

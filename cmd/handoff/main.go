package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Hans2573/OpenCode-Handoff/internal/config"
	handoffruntime "github.com/Hans2573/OpenCode-Handoff/internal/runtime"
	"github.com/Hans2573/OpenCode-Handoff/internal/store"
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
	instanceLock, err := handoffruntime.AcquireInstanceLock("agent-handoff-engine")
	if err != nil {
		logger.Error("start Handoff engine", "error", err)
		os.Exit(1)
	}
	defer instanceLock.Close()

	handoffStore, err := store.OpenSQLite(ctx, cfg.Store.Path)
	if err != nil {
		logger.Error("open handoff store", "error", err)
		os.Exit(1)
	}
	defer handoffStore.Close()
	service, err := handoffruntime.New(ctx, cfg, handoffStore, logger, nil)
	if err != nil {
		logger.Error("prepare Handoff service", "error", err)
		os.Exit(1)
	}

	logger.Info("OpenCode Handoff starting", "version", version, "opencode", cfg.OpenCode.BaseURL)
	if err := service.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("OpenCode Handoff stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("OpenCode Handoff stopped")
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

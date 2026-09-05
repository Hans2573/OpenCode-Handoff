package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Hans2573/OpenCode-Handoff/internal/desktop"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	workingDirectory, err := os.Getwd()
	if err != nil {
		fatalDesktop("读取工作目录", err)
	}
	executablePath, executableErr := os.Executable()
	if executableErr != nil {
		fatalDesktop("读取程序路径", executableErr)
	}
	executableDirectory := filepath.Dir(executablePath)
	localData := os.Getenv("LOCALAPPDATA")
	paths, err := desktop.BootstrapPaths(
		workingDirectory,
		executableDirectory,
		filepath.Dir(executableDirectory),
		filepath.Join(localData, "OpenCode Handoff"),
	)
	if err != nil {
		fatalDesktop("初始化桌面数据目录", err)
	}
	logger, logFile, logLevel, err := newDesktopLogger(paths.LogPath)
	if err != nil {
		fatalDesktop("创建日志文件", err)
	}
	defer logFile.Close()

	manager, err := desktop.NewManager(context.Background(), paths, logger)
	if err != nil {
		fatalDesktop("启动 Agent Handoff", err)
	}
	defer manager.Close()
	logLevel.Set(parseLogLevel(manager.GetSettings().LoggingLevel))
	service := NewAppService(manager, logLevel)

	var mainWindow *application.WebviewWindow
	app := application.New(application.Options{
		Name:        "Agent Handoff",
		Description: "Route local coding agent sessions to communication channels",
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.hans2573.agent-handoff",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if mainWindow != nil {
					mainWindow.Show().Focus()
				}
			},
		},
		Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
	})

	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "main", Title: "Agent Handoff", Width: 1500, Height: 920,
		MinWidth: 1120, MinHeight: 700, Hidden: hasArgument("--hidden"),
		BackgroundColour: application.NewRGB(11, 17, 24), URL: "/",
	})
	service.attach(app, mainWindow)
	mainWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		mainWindow.Hide()
		event.Cancel()
	})

	tray := app.SystemTray.New()
	tray.SetTooltip("Agent Handoff")
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(icons.SystrayMacTemplate)
	} else {
		tray.SetIcon(appIcon)
	}
	tray.OnClick(func() { mainWindow.Show().Focus() })
	menu := app.Menu.New()
	menu.Add("打开 Agent Handoff").OnClick(func(*application.Context) { mainWindow.Show().Focus() })
	menu.Add("隐藏窗口").OnClick(func(*application.Context) { mainWindow.Hide() })
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)

	if err := app.Run(); err != nil {
		logger.Error("desktop application stopped", "error", err)
		os.Exit(1)
	}
}

func hasArgument(expected string) bool {
	for _, argument := range os.Args[1:] {
		if strings.EqualFold(argument, expected) {
			return true
		}
	}
	return false
}

func newDesktopLogger(path string) (*slog.Logger, *os.File, *slog.LevelVar, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, nil, err
	}
	// Windows GUI builds may have no stderr handle. Write the log first so an
	// unavailable console cannot prevent persistent startup diagnostics.
	writer := io.MultiWriter(file, os.Stderr)
	level := &slog.LevelVar{}
	level.Set(slog.LevelInfo)
	return slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level})), file, level, nil
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func fatalDesktop(action string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "%s失败：%v\n", action, err)
	os.Exit(1)
}

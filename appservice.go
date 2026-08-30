package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hans2573/OpenCode-Handoff/internal/desktop"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type AppService struct {
	manager  *desktop.Manager
	app      *application.App
	window   *application.WebviewWindow
	logLevel *slog.LevelVar
}

func NewAppService(manager *desktop.Manager, logLevel *slog.LevelVar) *AppService {
	return &AppService{manager: manager, logLevel: logLevel}
}

func (s *AppService) attach(app *application.App, window *application.WebviewWindow) {
	s.app = app
	s.window = window
}

func (s *AppService) GetDashboard() (desktop.Dashboard, error) {
	return s.manager.GetDashboard()
}

func (s *AppService) RefreshProjects() (desktop.Dashboard, error) {
	if err := s.manager.RefreshProjects(); err != nil {
		return desktop.Dashboard{}, err
	}
	return s.manager.GetDashboard()
}

func (s *AppService) SetProjectRoute(projectID string, enabled bool) (desktop.Dashboard, error) {
	if err := s.manager.SetProjectRoute(projectID, enabled); err != nil {
		return desktop.Dashboard{}, err
	}
	return s.manager.GetDashboard()
}

func (s *AppService) RetryService() (desktop.Dashboard, error) {
	s.manager.RetryService()
	return s.manager.GetDashboard()
}

func (s *AppService) GetSettings() desktop.SettingsView {
	return s.manager.GetSettings()
}

func (s *AppService) SaveSettings(input desktop.SettingsInput) (desktop.SettingsView, error) {
	if err := s.manager.SaveSettings(input); err != nil {
		return desktop.SettingsView{}, err
	}
	settings := s.manager.GetSettings()
	if s.logLevel != nil {
		s.logLevel.Set(parseLogLevel(settings.LoggingLevel))
	}
	return settings, nil
}

func (s *AppService) GetEvents(search string, limit int) (desktop.EventPage, error) {
	return s.manager.GetEvents(search, limit)
}

func (s *AppService) ClearEvents() error {
	return s.manager.ClearEvents()
}

func (s *AppService) ExportEvents(search string) (string, error) {
	page, err := s.manager.GetEvents(search, 1000)
	if err != nil {
		return "", err
	}
	path, err := s.app.Dialog.SaveFile().
		SetFilename("agent-handoff-events-"+time.Now().Format("20060102-150405")+".json").
		AddFilter("JSON 文件", "*.json").
		PromptForSingleSelection()
	if err != nil || path == "" {
		return path, err
	}
	encoded, err := json.MarshalIndent(page.Items, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (s *AppService) OpenSession(sessionID, directory string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session ID is required")
	}
	s.app.Clipboard.SetText(sessionID)
	base, err := url.Parse(s.manager.OpenCodeURL())
	if err != nil {
		return err
	}
	query := base.Query()
	query.Set("directory", directory)
	query.Set("session", sessionID)
	base.RawQuery = query.Encode()
	return s.app.Browser.OpenURL(base.String())
}

func (s *AppService) OpenDataDirectory() error {
	return s.app.Env.OpenFileManager(s.manager.Paths().DataDir, false)
}

func (s *AppService) GetAutostart() (bool, error) {
	return s.app.Autostart.IsEnabled()
}

func (s *AppService) SetAutostart(enabled bool) error {
	if enabled {
		return s.app.Autostart.EnableWithOptions(application.AutostartOptions{
			Identifier: "com.hans2573.agent-handoff",
			Arguments:  []string{"--hidden"},
		})
	}
	return s.app.Autostart.Disable()
}

func (s *AppService) ShowWindow() {
	if s.window != nil {
		s.window.Show().Focus()
	}
}

func (s *AppService) HideWindow() {
	if s.window != nil {
		s.window.Hide()
	}
}

func (s *AppService) Quit() {
	s.app.Quit()
}

func (s *AppService) ConfigFileName() string {
	return filepath.Base(s.manager.Paths().ConfigPath)
}

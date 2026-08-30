package desktop

import "time"

type Dashboard struct {
	GeneratedAt time.Time         `json:"generatedAt"`
	Service     ServiceStatus     `json:"service"`
	Summary     DashboardSummary  `json:"summary"`
	Projects    []ProjectView     `json:"projects"`
	Sessions    []SessionView     `json:"sessions"`
	Agents      []IntegrationView `json:"agents"`
	Channels    []IntegrationView `json:"channels"`
}

type ServiceStatus struct {
	State           string `json:"state"`
	Message         string `json:"message"`
	EngineRunning   bool   `json:"engineRunning"`
	OpenCodeOnline  bool   `json:"openCodeOnline"`
	FeishuConnected bool   `json:"feishuConnected"`
	FeishuState     string `json:"feishuState"`
	FeishuMessage   string `json:"feishuMessage"`
	ConfigValid     bool   `json:"configValid"`
	OpenCodeURL     string `json:"openCodeUrl"`
}

type DashboardSummary struct {
	ConnectedProjects int `json:"connectedProjects"`
	RunningSessions   int `json:"runningSessions"`
	PendingActions    int `json:"pendingActions"`
	ConnectedChannels int `json:"connectedChannels"`
}

type ProjectView struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Directory          string    `json:"directory"`
	AgentID            string    `json:"agentId"`
	AgentName          string    `json:"agentName"`
	ChannelID          string    `json:"channelId"`
	ChannelName        string    `json:"channelName"`
	RouteEnabled       bool      `json:"routeEnabled"`
	Status             string    `json:"status"`
	LastSeen           time.Time `json:"lastSeen"`
	LastConversationAt time.Time `json:"lastConversationAt"`
}

type SessionView struct {
	ID                    string    `json:"id"`
	Title                 string    `json:"title"`
	ProjectName           string    `json:"projectName"`
	Directory             string    `json:"directory"`
	AgentName             string    `json:"agentName"`
	ChannelName           string    `json:"channelName"`
	Status                string    `json:"status"`
	StatusLabel           string    `json:"statusLabel"`
	StatusDetail          string    `json:"statusDetail"`
	RouteEnabled          bool      `json:"routeEnabled"`
	UpdatedAt             time.Time `json:"updatedAt"`
	BusyForSeconds        int64     `json:"busyForSeconds"`
	SinceLastInputSeconds int64     `json:"sinceLastInputSeconds"`
	LastInput             string    `json:"lastInput"`
	HasLastInput          bool      `json:"hasLastInput"`
	CurrentModel          string    `json:"currentModel"`
	CurrentVariant        string    `json:"currentVariant"`
}

type IntegrationView struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	StatusLabel string `json:"statusLabel"`
	Endpoint    string `json:"endpoint"`
	Available   bool   `json:"available"`
	ComingSoon  bool   `json:"comingSoon"`
}

type SettingsView struct {
	Paths                Paths             `json:"paths"`
	OpenCodeBaseURL      string            `json:"openCodeBaseUrl"`
	OpenCodeDirectory    string            `json:"openCodeDirectory"`
	OpenCodeUsername     string            `json:"openCodeUsername"`
	OpenCodePasswordSet  bool              `json:"openCodePasswordSet"`
	AllowRemote          bool              `json:"allowRemote"`
	FeishuAppID          string            `json:"feishuAppId"`
	FeishuAppSecretSet   bool              `json:"feishuAppSecretSet"`
	FeishuChatID         string            `json:"feishuChatId"`
	AllowedUsers         []string          `json:"allowedUsers"`
	PollingInterval      string            `json:"pollingInterval"`
	MaxOutputChars       int               `json:"maxOutputChars"`
	NotifyIdle           bool              `json:"notifyIdle"`
	NotifyError          bool              `json:"notifyError"`
	NotifyQuestion       bool              `json:"notifyQuestion"`
	NotifyPermission     bool              `json:"notifyPermission"`
	LoggingLevel         string            `json:"loggingLevel"`
	EnvironmentOverrides map[string]string `json:"environmentOverrides"`
	ConfigError          string            `json:"configError"`
}

type SettingsInput struct {
	OpenCodeBaseURL       string   `json:"openCodeBaseUrl"`
	OpenCodeDirectory     string   `json:"openCodeDirectory"`
	OpenCodeUsername      string   `json:"openCodeUsername"`
	OpenCodePassword      string   `json:"openCodePassword"`
	ClearOpenCodePassword bool     `json:"clearOpenCodePassword"`
	AllowRemote           bool     `json:"allowRemote"`
	FeishuAppID           string   `json:"feishuAppId"`
	FeishuAppSecret       string   `json:"feishuAppSecret"`
	FeishuChatID          string   `json:"feishuChatId"`
	AllowedUsers          []string `json:"allowedUsers"`
	PollingInterval       string   `json:"pollingInterval"`
	MaxOutputChars        int      `json:"maxOutputChars"`
	NotifyIdle            bool     `json:"notifyIdle"`
	NotifyError           bool     `json:"notifyError"`
	NotifyQuestion        bool     `json:"notifyQuestion"`
	NotifyPermission      bool     `json:"notifyPermission"`
	LoggingLevel          string   `json:"loggingLevel"`
}

type EventPage struct {
	Items []EventView `json:"items"`
}

type EventView struct {
	ID        int64          `json:"id"`
	Level     string         `json:"level"`
	Type      string         `json:"type"`
	Source    string         `json:"source"`
	Message   string         `json:"message"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

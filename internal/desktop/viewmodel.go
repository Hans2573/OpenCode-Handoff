package desktop

import "time"

type Dashboard struct {
	GeneratedAt            time.Time              `json:"generatedAt"`
	Service                ServiceStatus          `json:"service"`
	Summary                DashboardSummary       `json:"summary"`
	Projects               []ProjectView          `json:"projects"`
	Sessions               []SessionView          `json:"sessions"`
	ExecutionRuns          []ExecutionRunView     `json:"executionRuns"`
	ExecutionSessions      []ExecutionSessionView `json:"executionSessions"`
	ExecutionRetentionDays int                    `json:"executionRetentionDays"`
	Agents                 []IntegrationView      `json:"agents"`
	Channels               []IntegrationView      `json:"channels"`
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
	ID                     string    `json:"id"`
	Title                  string    `json:"title"`
	ProjectName            string    `json:"projectName"`
	Directory              string    `json:"directory"`
	AgentName              string    `json:"agentName"`
	ChannelName            string    `json:"channelName"`
	Status                 string    `json:"status"`
	StatusLabel            string    `json:"statusLabel"`
	StatusDetail           string    `json:"statusDetail"`
	RouteEnabled           bool      `json:"routeEnabled"`
	UpdatedAt              time.Time `json:"updatedAt"`
	BusyForSeconds         int64     `json:"busyForSeconds"`
	SinceLastInputSeconds  int64     `json:"sinceLastInputSeconds"`
	LastInput              string    `json:"lastInput"`
	HasLastInput           bool      `json:"hasLastInput"`
	CurrentModel           string    `json:"currentModel"`
	CurrentVariant         string    `json:"currentVariant"`
	LatestExecutionSeconds int64     `json:"latestExecutionSeconds"`
	TotalExecutionSeconds  int64     `json:"totalExecutionSeconds"`
	ExecutionCount         int       `json:"executionCount"`
	GoalLoopID             string    `json:"goalLoopId"`
	GoalLoopActive         bool      `json:"goalLoopActive"`
}

type ExecutionRunView struct {
	ID              int64     `json:"id"`
	SessionID       string    `json:"sessionId"`
	SessionTitle    string    `json:"sessionTitle"`
	Directory       string    `json:"directory"`
	ProjectName     string    `json:"projectName"`
	DurationSeconds int64     `json:"durationSeconds"`
	StartedAt       time.Time `json:"startedAt"`
	EndedAt         time.Time `json:"endedAt"`
	EndReason       string    `json:"endReason"`
	StatusLabel     string    `json:"statusLabel"`
	Active          bool      `json:"active"`
}

type ExecutionSessionView struct {
	SessionID              string `json:"sessionId"`
	SessionTitle           string `json:"sessionTitle"`
	Directory              string `json:"directory"`
	ProjectName            string `json:"projectName"`
	LatestExecutionSeconds int64  `json:"latestExecutionSeconds"`
	TotalExecutionSeconds  int64  `json:"totalExecutionSeconds"`
	ExecutionCount         int    `json:"executionCount"`
	StatusLabel            string `json:"statusLabel"`
	Active                 bool   `json:"active"`
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
	Paths                  Paths             `json:"paths"`
	FileSizes              FileSizes         `json:"fileSizes"`
	OpenCodeBaseURL        string            `json:"openCodeBaseUrl"`
	OpenCodeDirectory      string            `json:"openCodeDirectory"`
	OpenCodeUsername       string            `json:"openCodeUsername"`
	OpenCodePasswordSet    bool              `json:"openCodePasswordSet"`
	AllowRemote            bool              `json:"allowRemote"`
	FeishuAppID            string            `json:"feishuAppId"`
	FeishuAppSecretSet     bool              `json:"feishuAppSecretSet"`
	FeishuChatID           string            `json:"feishuChatId"`
	AllowedUsers           []string          `json:"allowedUsers"`
	PollingInterval        string            `json:"pollingInterval"`
	MaxOutputChars         int               `json:"maxOutputChars"`
	NotifyIdle             bool              `json:"notifyIdle"`
	NotifyError            bool              `json:"notifyError"`
	NotifyQuestion         bool              `json:"notifyQuestion"`
	NotifyPermission       bool              `json:"notifyPermission"`
	LoggingLevel           string            `json:"loggingLevel"`
	ExecutionRetentionDays int               `json:"executionRetentionDays"`
	EnvironmentOverrides   map[string]string `json:"environmentOverrides"`
	ConfigError            string            `json:"configError"`
}

type FileSizes struct {
	Config int64 `json:"config"`
	Store  int64 `json:"store"`
	Log    int64 `json:"log"`
}

type SettingsInput struct {
	OpenCodeBaseURL        string   `json:"openCodeBaseUrl"`
	OpenCodeDirectory      string   `json:"openCodeDirectory"`
	OpenCodeUsername       string   `json:"openCodeUsername"`
	OpenCodePassword       string   `json:"openCodePassword"`
	ClearOpenCodePassword  bool     `json:"clearOpenCodePassword"`
	AllowRemote            bool     `json:"allowRemote"`
	FeishuAppID            string   `json:"feishuAppId"`
	FeishuAppSecret        string   `json:"feishuAppSecret"`
	FeishuChatID           string   `json:"feishuChatId"`
	AllowedUsers           []string `json:"allowedUsers"`
	PollingInterval        string   `json:"pollingInterval"`
	MaxOutputChars         int      `json:"maxOutputChars"`
	NotifyIdle             bool     `json:"notifyIdle"`
	NotifyError            bool     `json:"notifyError"`
	NotifyQuestion         bool     `json:"notifyQuestion"`
	NotifyPermission       bool     `json:"notifyPermission"`
	LoggingLevel           string   `json:"loggingLevel"`
	ExecutionRetentionDays int      `json:"executionRetentionDays"`
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

type GoalLoopPage struct {
	GeneratedAt time.Time          `json:"generatedAt"`
	Loops       []GoalLoopView     `json:"loops"`
	Approvals   []LoopApprovalView `json:"approvals"`
}

type GoalLoopView struct {
	ID                            string    `json:"id"`
	Name                          string    `json:"name"`
	Goal                          string    `json:"goal"`
	ProjectID                     string    `json:"projectId"`
	ProjectName                   string    `json:"projectName"`
	Directory                     string    `json:"directory"`
	AgentID                       string    `json:"agentId"`
	AgentName                     string    `json:"agentName"`
	ModelProviderID               string    `json:"modelProviderId"`
	ModelID                       string    `json:"modelId"`
	ModelName                     string    `json:"modelName"`
	ModelVariant                  string    `json:"modelVariant"`
	SessionID                     string    `json:"sessionId"`
	AttachedSession               bool      `json:"attachedSession"`
	AutomationMode                string    `json:"automationMode"`
	PermissionApprovalMode        string    `json:"permissionApprovalMode"`
	AllowedDirectories            []string  `json:"allowedDirectories"`
	SupervisorModelProviderID     string    `json:"supervisorModelProviderId"`
	SupervisorModelID             string    `json:"supervisorModelId"`
	SupervisorModelName           string    `json:"supervisorModelName"`
	SupervisorModelVariant        string    `json:"supervisorModelVariant"`
	SupervisorSessionID           string    `json:"supervisorSessionId"`
	PendingRequestID              string    `json:"pendingRequestId"`
	PendingRequestType            string    `json:"pendingRequestType"`
	Status                        string    `json:"status"`
	StatusLabel                   string    `json:"statusLabel"`
	RequireCompletionConfirmation bool      `json:"requireCompletionConfirmation"`
	FailureLimit                  int       `json:"failureLimit"`
	ConsecutiveFailures           int       `json:"consecutiveFailures"`
	CycleCount                    int       `json:"cycleCount"`
	LastError                     string    `json:"lastError"`
	RetryAt                       time.Time `json:"retryAt"`
	CreatedAt                     time.Time `json:"createdAt"`
	UpdatedAt                     time.Time `json:"updatedAt"`
	CompletedAt                   time.Time `json:"completedAt"`
}

type GoalLoopInput struct {
	Goal                          string   `json:"goal"`
	ProjectID                     string   `json:"projectId"`
	AgentID                       string   `json:"agentId"`
	ModelProviderID               string   `json:"modelProviderId"`
	ModelID                       string   `json:"modelId"`
	ModelVariant                  string   `json:"modelVariant"`
	SessionID                     string   `json:"sessionId"`
	AutomationMode                string   `json:"automationMode"`
	PermissionApprovalMode        string   `json:"permissionApprovalMode"`
	AllowedDirectories            []string `json:"allowedDirectories"`
	SupervisorModelProviderID     string   `json:"supervisorModelProviderId"`
	SupervisorModelID             string   `json:"supervisorModelId"`
	SupervisorModelVariant        string   `json:"supervisorModelVariant"`
	FailureLimit                  int      `json:"failureLimit"`
	RequireCompletionConfirmation bool     `json:"requireCompletionConfirmation"`
	GoalCommandConfirmed          bool     `json:"goalCommandConfirmed"`
	StartNow                      bool     `json:"startNow"`
}

type GoalModelView struct {
	ProviderID   string   `json:"providerId"`
	ProviderName string   `json:"providerName"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Variants     []string `json:"variants"`
}

type SessionModelView struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	Variant    string `json:"variant"`
}

type GoalLoopEventView struct {
	ID        int64          `json:"id"`
	Type      string         `json:"type"`
	Message   string         `json:"message"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

type LoopApprovalView struct {
	ID             string                 `json:"id"`
	Type           string                 `json:"type"`
	SessionID      string                 `json:"sessionId"`
	LoopID         string                 `json:"loopId"`
	Autonomous     bool                   `json:"autonomous"`
	ProjectName    string                 `json:"projectName"`
	Directory      string                 `json:"directory"`
	Questions      []ApprovalQuestionView `json:"questions"`
	PermissionName string                 `json:"permissionName"`
	Patterns       []string               `json:"patterns"`
}

type ApprovalQuestionView struct {
	Question string               `json:"question"`
	Header   string               `json:"header"`
	Options  []ApprovalOptionView `json:"options"`
	Multiple bool                 `json:"multiple"`
	Custom   bool                 `json:"custom"`
}

type ApprovalOptionView struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type QuestionReplyInput struct {
	RequestID string     `json:"requestId"`
	Directory string     `json:"directory"`
	Answers   [][]string `json:"answers"`
	Reject    bool       `json:"reject"`
}

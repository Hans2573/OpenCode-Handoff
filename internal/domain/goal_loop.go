package domain

import "time"

const (
	GoalLoopDraft                = "draft"
	GoalLoopRunning              = "running"
	GoalLoopRetrying             = "retrying"
	GoalLoopWaitingApproval      = "waiting_approval"
	GoalLoopWaitingTakeover      = "waiting_takeover"
	GoalLoopDeciding             = "deciding"
	GoalLoopPaused               = "paused"
	GoalLoopAwaitingConfirmation = "awaiting_confirmation"
	GoalLoopCompleted            = "completed"
	GoalLoopBlocked              = "blocked"
	GoalLoopTerminated           = "terminated"
)

const (
	GoalLoopAutonomous = "autonomous"
	GoalLoopManual     = "manual"
)

const (
	GoalPermissionAI       = "ai"
	GoalPermissionAllowAll = "allow_all"
)

type GoalLoop struct {
	ID                            string
	Name                          string
	Goal                          string
	ProjectID                     string
	ProjectName                   string
	Directory                     string
	AgentID                       string
	AgentName                     string
	ModelProviderID               string
	ModelID                       string
	ModelName                     string
	ModelVariant                  string
	SessionID                     string
	AttachedSession               bool
	AutomationMode                string
	PermissionApprovalMode        string
	AllowedDirectories            []string
	SupervisorModelProviderID     string
	SupervisorModelID             string
	SupervisorModelName           string
	SupervisorModelVariant        string
	SupervisorSessionID           string
	PendingRequestID              string
	PendingRequestType            string
	SupervisorLastMessageID       string
	PendingFeedback               string
	Status                        string
	RequireCompletionConfirmation bool
	FailureLimit                  int
	ConsecutiveFailures           int
	CycleCount                    int
	LastAssistantMessageID        string
	PendingUserMessageID          string
	PromptSubmittedAt             time.Time
	PromptIdleSince               time.Time
	LastError                     string
	RetryAt                       time.Time
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
	CompletedAt                   time.Time
}

type GoalLoopEvent struct {
	ID        int64
	LoopID    string
	Type      string
	Message   string
	Metadata  map[string]any
	CreatedAt time.Time
}

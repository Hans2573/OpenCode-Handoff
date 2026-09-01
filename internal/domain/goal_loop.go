package domain

import "time"

const (
	GoalLoopDraft                = "draft"
	GoalLoopRunning              = "running"
	GoalLoopRetrying             = "retrying"
	GoalLoopWaitingApproval      = "waiting_approval"
	GoalLoopPaused               = "paused"
	GoalLoopAwaitingConfirmation = "awaiting_confirmation"
	GoalLoopCompleted            = "completed"
	GoalLoopTerminated           = "terminated"
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
	Status                        string
	RequireCompletionConfirmation bool
	FailureLimit                  int
	ConsecutiveFailures           int
	CycleCount                    int
	LastAssistantMessageID        string
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
	CreatedAt time.Time
}

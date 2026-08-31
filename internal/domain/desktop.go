package domain

import "time"

type AgentProject struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agentId"`
	Name      string    `json:"name"`
	Directory string    `json:"directory"`
	Enabled   bool      `json:"enabled"`
	LastSeen  time.Time `json:"lastSeen"`
}

type ProjectRoute struct {
	ProjectID    string    `json:"projectId"`
	AgentID      string    `json:"agentId"`
	Name         string    `json:"name"`
	Directory    string    `json:"directory"`
	ChannelID    string    `json:"channelId"`
	RouteEnabled bool      `json:"routeEnabled"`
	LastSeen     time.Time `json:"lastSeen"`
}

type EventLog struct {
	ID        int64          `json:"id"`
	Level     string         `json:"level"`
	Type      string         `json:"type"`
	Source    string         `json:"source"`
	Message   string         `json:"message"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

// SessionExecutionRun is one autonomous execution round. A round starts when
// an Agent begins working after user input and ends when it becomes idle or
// asks for human intervention.
type SessionExecutionRun struct {
	ID              int64     `json:"id"`
	SessionID       string    `json:"sessionId"`
	Directory       string    `json:"directory"`
	ProjectName     string    `json:"projectName"`
	SessionTitle    string    `json:"sessionTitle"`
	StartedAt       time.Time `json:"startedAt"`
	EndedAt         time.Time `json:"endedAt"`
	DurationSeconds int64     `json:"durationSeconds"`
	EndReason       string    `json:"endReason"`
}

type SessionExecutionStats struct {
	SessionID              string
	Directory              string
	ProjectName            string
	SessionTitle           string
	ExecutionCount         int
	LatestExecutionSeconds int64
	TotalExecutionSeconds  int64
}

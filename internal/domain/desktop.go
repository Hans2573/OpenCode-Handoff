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

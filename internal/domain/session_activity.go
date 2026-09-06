package domain

import "time"

const (
	SessionActivityNormal    = "normal"
	SessionActivitySuspected = "suspected"
	SessionActivityStalled   = "stalled"
	SessionActivityWaiting   = "waiting"
)

// SessionActivitySnapshot stores only the latest observed activity for a root
// OpenCode session. New observations overwrite the same row; this is not an
// activity history.
type SessionActivitySnapshot struct {
	SessionID          string
	Directory          string
	Fingerprint        string
	SessionStatus      string
	Level              string
	LastActivityAt     time.Time
	OperationStartedAt time.Time
	OperationType      string
	OperationSummary   string
	OperationStatus    string
	SourceSessionID    string
	SourceSessionTitle string
	SourceAgent        string
	SourceIsSubagent   bool
	SnoozedUntil       time.Time
	UpdatedAt          time.Time
}

package domain

import "time"

type SlowOperation struct {
	ID               int64
	RootSessionID    string
	SourceSessionID  string
	Directory        string
	MessageID        string
	PartID           string
	Tool             string
	InputPreview     string
	InputHash        string
	Summary          string
	Status           string
	SourceTitle      string
	SourceAgent      string
	SourceIsSubagent bool
	StartedAt        time.Time
	EndedAt          time.Time
	DurationSeconds  int64
	UpdatedAt        time.Time
}

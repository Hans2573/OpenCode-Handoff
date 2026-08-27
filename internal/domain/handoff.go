package domain

import "time"

type HandoffType string

const (
	HandoffFinished   HandoffType = "FINISHED"
	HandoffQuestion   HandoffType = "QUESTION"
	HandoffPermission HandoffType = "PERMISSION"
	HandoffError      HandoffType = "ERROR"
	HandoffStalled    HandoffType = "STALLED"
)

type HandoffStatus string

const (
	StatusOpen    HandoffStatus = "OPEN"
	StatusResumed HandoffStatus = "RESUMED"
	StatusClosed  HandoffStatus = "CLOSED"
	StatusExpired HandoffStatus = "EXPIRED"
)

type Handoff struct {
	ID                     string
	SessionID              string
	SessionName            string
	Directory              string
	ProjectName            string
	FeishuChatID           string
	FeishuMessageID        string
	Type                   HandoffType
	LastAssistantMessageID string
	LastAssistantText      string
	ErrorText              string
	Status                 HandoffStatus
	CreatedAt              time.Time
	ResolvedAt             *time.Time
}

type MessageRef struct {
	ChatID    string
	MessageID string
}

type UserReply struct {
	MessageID       string
	ParentMessageID string
	ChatID          string
	SenderID        string
	SenderIDs       []string
	Text            string
}

type ChannelBinding struct {
	ChatID    string
	UserIDs   []string
	CreatedAt time.Time
}

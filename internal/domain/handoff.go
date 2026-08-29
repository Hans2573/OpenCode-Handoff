package domain

import "time"

type HandoffType string

const (
	HandoffFinished   HandoffType = "FINISHED"
	HandoffQuestion   HandoffType = "QUESTION"
	HandoffPermission HandoffType = "PERMISSION"
	HandoffSession    HandoffType = "SESSION_CREATED"
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
	QuestionID             string
	Questions              []Question
	PermissionID           string
	Permission             Permission
	Status                 HandoffStatus
	CreatedAt              time.Time
	ResolvedAt             *time.Time
}

type Question struct {
	Text      string
	Header    string
	Options   []QuestionOption
	Multiple  bool
	Custom    bool
	CustomSet bool
}

func (q Question) AllowsCustom() bool {
	return !q.CustomSet || q.Custom
}

type QuestionOption struct {
	Label       string
	Description string
}

type Permission struct {
	Name     string
	Patterns []string
	Always   []string
	Metadata map[string]any
}

type MessageRef struct {
	ChatID    string
	MessageID string
}

type Project struct {
	ID        string
	Name      string
	Directory string
}

type ProjectPage struct {
	Projects   []Project
	Page       int
	TotalPages int
	Total      int
}

type RunningSession struct {
	SessionID        string
	SessionName      string
	ProjectName      string
	Directory        string
	State            string
	LastUserText     string
	LastUserInputAt  time.Time
	RunningFor       time.Duration
	HasLastUserInput bool
}

type RunningSessions struct {
	Items           []RunningSession
	Total           int
	ScannedProjects int
	FailedProjects  int
}

type UserReply struct {
	MessageID        string
	ParentMessageID  string
	ChatID           string
	SenderID         string
	SenderIDs        []string
	Text             string
	QuestionAnswers  [][]string
	RejectQuestion   bool
	PermissionReply  string
	AbortSession     bool
	ListProjects     bool
	ProjectPage      int
	CreateSession    bool
	ProjectDirectory string
	ListRunning      bool
	CardAction       bool
	Result           chan error
}

type ChannelBinding struct {
	ChatID    string
	UserIDs   []string
	CreatedAt time.Time
}

package domain

import "time"

type HandoffType string

const (
	HandoffFinished       HandoffType = "FINISHED"
	HandoffQuestion       HandoffType = "QUESTION"
	HandoffPermission     HandoffType = "PERMISSION"
	HandoffSession        HandoffType = "SESSION_CREATED"
	HandoffError          HandoffType = "ERROR"
	HandoffStalled        HandoffType = "STALLED"
	HandoffGoalCompletion HandoffType = "GOAL_COMPLETION"
	HandoffGoalStatus     HandoffType = "GOAL_STATUS"
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
	ModelName              string
	ModelProviderID        string
	ModelID                string
	ModelVariant           string
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

type ModelTarget string

const (
	ModelTargetBrowse ModelTarget = "browse"
	ModelTargetCreate ModelTarget = "create"
	ModelTargetSwitch ModelTarget = "switch"
)

type Model struct {
	ProviderID   string
	ProviderName string
	ID           string
	Name         string
	Status       string
	Variants     []string
	Reasoning    bool
	Attachment   bool
	ContextLimit int64
}

type ModelContext struct {
	Target           ModelTarget
	ProjectDirectory string
	SessionID        string
	SessionName      string
}

type ModelProvider struct {
	ID    string
	Name  string
	Count int
}

type RecentModel struct {
	Model   Model
	Variant string
}

type ModelPage struct {
	Models       []Model
	Recent       []RecentModel
	Providers    []ModelProvider
	Page         int
	TotalPages   int
	Total        int
	Query        string
	ProviderID   string
	ProviderName string
	Home         bool
	Context      ModelContext
}

type ModelVariantPage struct {
	Model   Model
	Context ModelContext
}

type SessionModel struct {
	ProviderID string
	ModelID    string
	ModelName  string
	Variant    string
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
	CurrentModel     string
	CurrentVariant   string
}

type RunningSessions struct {
	Items           []RunningSession
	Total           int
	ScannedProjects int
	FailedProjects  int
}

type AssistantOutputDetail struct {
	SessionID   string
	SessionName string
	Content     string
}

type UserReply struct {
	MessageID         string
	ParentMessageID   string
	ChatID            string
	SenderID          string
	SenderIDs         []string
	Text              string
	QuestionAnswers   [][]string
	RejectQuestion    bool
	PermissionReply   string
	AbortSession      bool
	ListProjects      bool
	ProjectPage       int
	CreateSession     bool
	ProjectDirectory  string
	ListRunning       bool
	ListModels        bool
	ModelPage         int
	ModelQuery        string
	ModelProviderID   string
	ModelContext      ModelContext
	ListModelVariants bool
	ApplyModel        bool
	ViewOutput        bool
	GoalComplete      bool
	GoalContinue      bool
	HandoffID         string
	ProviderID        string
	ModelID           string
	ModelName         string
	ModelVariant      string
	CardAction        bool
	Result            chan error
}

type ChannelBinding struct {
	ChatID    string
	UserIDs   []string
	CreatedAt time.Time
}

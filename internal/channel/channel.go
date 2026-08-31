package channel

import (
	"context"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

type Channel interface {
	SendHandoff(context.Context, domain.Handoff) (domain.MessageRef, error)
	ReplyProjects(context.Context, string, domain.ProjectPage) error
	ReplyModels(context.Context, string, domain.ModelPage) error
	ReplyModelVariants(context.Context, string, domain.ModelVariantPage) error
	ReplyRunningSessions(context.Context, string, domain.RunningSessions) error
	ReplyAssistantOutput(context.Context, string, domain.AssistantOutputDetail) error
	Reply(context.Context, string, string) error
	ReplyWithRef(context.Context, string, string) (domain.MessageRef, error)
	Receive(context.Context) (<-chan domain.UserReply, error)
}

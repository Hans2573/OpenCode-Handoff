package channel

import (
	"context"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

type Channel interface {
	SendHandoff(context.Context, domain.Handoff) (domain.MessageRef, error)
	Reply(context.Context, string, string) error
	Receive(context.Context) (<-chan domain.UserReply, error)
}

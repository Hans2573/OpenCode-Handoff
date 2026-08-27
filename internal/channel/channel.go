package channel

import (
	"context"

	"github.com/xiaohang2/opencode-handoff/internal/domain"
)

type Channel interface {
	SendHandoff(context.Context, domain.Handoff) (domain.MessageRef, error)
	Reply(context.Context, string, string) error
	Receive(context.Context) (<-chan domain.UserReply, error)
}

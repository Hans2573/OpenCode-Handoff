package store

import (
	"context"
	"errors"

	"github.com/Hans2573/OpenCode-Handoff/internal/domain"
)

var (
	ErrDuplicate      = errors.New("handoff already exists")
	ErrNotFound       = errors.New("handoff not found")
	ErrAlreadyBound   = errors.New("channel is already bound")
	ErrAmbiguous      = errors.New("multiple sessions are waiting in this channel")
	ErrDuplicateReply = errors.New("channel reply was already processed")
)

type Store interface {
	Create(context.Context, domain.Handoff) error
	BindMessage(context.Context, string, domain.MessageRef) error
	DeleteUnbound(context.Context, string) error
	ClaimByMessage(context.Context, string, string) (domain.Handoff, error)
	ClaimOnlyOpenByChat(context.Context, string, string) (domain.Handoff, error)
	Reopen(context.Context, string) error
	CloseResolvedPermissions(context.Context, string, []string) error
	ClosePermission(context.Context, string) error
	ClaimSessionCreate(context.Context, string) error
	CompleteSessionCreate(context.Context, string, string) error
	ReleaseSessionCreate(context.Context, string) error
	GetPendingSessionModel(context.Context, string) (domain.SessionModel, error)
	SetPendingSessionModel(context.Context, string, domain.SessionModel) error
	ClearPendingSessionModel(context.Context, string) error
	RecordRecentModel(context.Context, domain.SessionModel) error
	ListRecentModels(context.Context, int) ([]domain.SessionModel, error)
	GetChannelBinding(context.Context) (domain.ChannelBinding, error)
	BindChannel(context.Context, domain.ChannelBinding) error
	Close() error
}

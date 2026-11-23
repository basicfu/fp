package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// recordingPublisher 记下发布的每一条事件，供断言推送。
type recordingPublisher struct {
	mu     sync.Mutex
	events []domain.RevokeEvent
}

func (p *recordingPublisher) Publish(_ context.Context, ev domain.RevokeEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return nil
}

func (p *recordingPublisher) last(t *testing.T) domain.RevokeEvent {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.events) == 0 {
		t.Fatal("没有发布任何事件")
	}
	return p.events[len(p.events)-1]
}

func wantDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

// recordingAppender 记下写进事件表的业务事件。
type recordingAppender struct {
	mu     sync.Mutex
	events []recordedAppEvent
}

type recordedAppEvent struct {
	AppID, UserID uuid.UUID
	Kind          string
	Payload       any
}

func (r *recordingAppender) Append(_ context.Context, appID, userID uuid.UUID, kind string, payload any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedAppEvent{AppID: appID, UserID: userID, Kind: kind, Payload: payload})
	return nil
}

func (r *recordingAppender) take() []recordedAppEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

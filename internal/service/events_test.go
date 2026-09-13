package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

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

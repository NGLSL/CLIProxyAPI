package usage

import (
	"context"
	"sync"
	"testing"
)

type collectingPlugin struct {
	mu      sync.Mutex
	records []Record
}

func (p *collectingPlugin) HandleUsage(ctx context.Context, record Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, record)
}

func (p *collectingPlugin) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.records)
}

func TestManagerCanRestartAfterStopAndContinueDraining(t *testing.T) {
	manager := NewManager(16)
	plugin := &collectingPlugin{}
	manager.Register(plugin)

	manager.Start(context.Background())
	manager.Publish(context.Background(), Record{APIKey: "first"})
	manager.Stop()

	if got := plugin.count(); got != 1 {
		t.Fatalf("records after first stop = %d, want 1", got)
	}

	manager.Start(context.Background())
	manager.Publish(context.Background(), Record{APIKey: "second"})
	manager.Stop()

	if got := plugin.count(); got != 2 {
		t.Fatalf("records after restart = %d, want 2", got)
	}
}

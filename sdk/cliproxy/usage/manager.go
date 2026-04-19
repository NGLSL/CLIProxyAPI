package usage

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Record contains the usage statistics captured for a single provider request.
type Record struct {
	Provider         string
	Model            string
	APIKey           string
	AuthID           string
	AuthIndex        string
	Source           string
	RequestedAt      time.Time
	Latency          time.Duration
	FirstByteLatency time.Duration
	Failed           bool
	ChunkCount       int64
	ResponseBytes    int64
	APIResponseBytes int64
	Detail           Detail
}

// Detail holds the token usage breakdown.
type Detail struct {
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	CachedTokens    int64
	TotalTokens     int64
}

// Plugin consumes usage records emitted by the proxy runtime.
type Plugin interface {
	HandleUsage(ctx context.Context, record Record)
}

type queueItem struct {
	ctx    context.Context
	record Record
}

// Manager maintains a queue of usage records and delivers them to registered plugins.
type Manager struct {
	cancel   context.CancelFunc
	workerWG sync.WaitGroup

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []queueItem
	closed  bool
	running bool

	pluginsMu sync.RWMutex
	plugins   []Plugin
}

// NewManager constructs a manager with a buffered queue.
func NewManager(buffer int) *Manager {
	m := &Manager{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// Start launches the background dispatcher. Calling Start multiple times is safe.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return
	}
	m.closed = false
	m.running = true
	var workerCtx context.Context
	workerCtx, m.cancel = context.WithCancel(ctx)
	m.workerWG.Add(1)
	go func() {
		defer m.workerWG.Done()
		m.run(workerCtx)
	}()
}

// Stop stops the dispatcher and drains the queue.
func (m *Manager) Stop() {
	if m == nil {
		return
	}

	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	cancel := m.cancel
	m.closed = true
	m.running = false
	m.cancel = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.cond.Broadcast()
	m.workerWG.Wait()
}

// Reset stops the dispatcher and clears pending state so the manager can be reused.
func (m *Manager) Reset() {
	if m == nil {
		return
	}
	m.Stop()
	m.mu.Lock()
	m.queue = nil
	m.closed = false
	m.running = false
	m.cancel = nil
	m.mu.Unlock()
}

// SetDefaultManager replaces the global usage manager instance.
func SetDefaultManager(manager *Manager) {
	if manager == nil {
		manager = NewManager(512)
	}
	defaultManager = manager
}

// ResetDefaultManager resets the global usage manager to a fresh reusable instance.
func ResetDefaultManager() { SetDefaultManager(NewManager(512)) }

// ResetDefaultManagerForTest swaps the global usage manager for tests and restores it afterwards.
func ResetDefaultManagerForTest(tb interface{ Cleanup(func()) }) *Manager {
	previous := DefaultManager()
	manager := NewManager(512)
	SetDefaultManager(manager)
	if tb != nil {
		tb.Cleanup(func() {
			manager.Stop()
			SetDefaultManager(previous)
		})
	}
	return manager
}

// Wait blocks until the dispatcher has drained the queue and exited.
func (m *Manager) Wait() {
	if m == nil {
		return
	}
	m.workerWG.Wait()
}

// Register appends a plugin to the delivery list.
func (m *Manager) Register(plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	m.pluginsMu.Lock()
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// Publish enqueues a usage record for processing. If no plugin is registered
// the record will be discarded downstream.
func (m *Manager) Publish(ctx context.Context, record Record) {
	if m == nil {
		return
	}
	// ensure worker is running even if Start was not called explicitly
	m.Start(context.Background())
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.queue = append(m.queue, queueItem{ctx: ctx, record: record})
	m.mu.Unlock()
	m.cond.Signal()
}

func (m *Manager) run(ctx context.Context) {
	for {
		m.mu.Lock()
		for !m.closed && len(m.queue) == 0 {
			m.cond.Wait()
		}
		if len(m.queue) == 0 && m.closed {
			m.mu.Unlock()
			return
		}
		item := m.queue[0]
		m.queue = m.queue[1:]
		m.mu.Unlock()
		m.dispatch(item)
	}
}

func (m *Manager) dispatch(item queueItem) {
	m.pluginsMu.RLock()
	plugins := make([]Plugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.pluginsMu.RUnlock()
	if len(plugins) == 0 {
		return
	}
	for _, plugin := range plugins {
		if plugin == nil {
			continue
		}
		safeInvoke(plugin, item.ctx, item.record)
	}
}

func safeInvoke(plugin Plugin, ctx context.Context, record Record) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("usage: plugin panic recovered: %v", r)
		}
	}()
	plugin.HandleUsage(ctx, record)
}

var defaultManager = NewManager(512)

// DefaultManager returns the global usage manager instance.
func DefaultManager() *Manager { return defaultManager }

// RegisterPlugin registers a plugin on the default manager.
func RegisterPlugin(plugin Plugin) { DefaultManager().Register(plugin) }

// PublishRecord publishes a record using the default manager.
func PublishRecord(ctx context.Context, record Record) { DefaultManager().Publish(ctx, record) }

// StartDefault starts the default manager's dispatcher.
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault stops the default manager's dispatcher.
func StopDefault() { DefaultManager().Stop() }

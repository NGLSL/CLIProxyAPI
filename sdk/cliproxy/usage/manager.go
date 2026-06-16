package usage

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Record contains the usage statistics captured for a single provider request.
type Record struct {
	Provider string
	// ExecutorType stores the concrete executor type that handled the request.
	// Plugin sinks use this to attribute traffic to a specific executor
	// implementation (e.g. "gemini-cli", "claude-cli", "codex-ws").
	ExecutorType string
	Model        string
	Alias        string
	APIKey       string
	AuthID       string
	AuthIndex    string
	// AuthType stores the credential type that backed the request (e.g.
	// "oauth", "api-key"). Empty means the value was not captured.
	AuthType string
	Source   string
	// ReasoningEffort stores the translated upstream thinking level for request event logs.
	ReasoningEffort string
	// ServiceTier stores the client-requested service tier for request event logs.
	ServiceTier string
	RequestedAt time.Time
	Latency     time.Duration
	// FirstByteLatency / APIFirstByteLatency preserve the fork's historical
	// TTFB measurements for the file-backed usage logger and are kept populated
	// alongside TTFT below for compatibility with both fork and upstream sinks.
	FirstByteLatency    time.Duration
	APIFirstByteLatency time.Duration
	// TTFT is the upstream-aligned time-to-first-token duration used by plugin
	// usage sinks. It mirrors FirstByteLatency semantically.
	TTFT time.Duration
	// Failed flags a terminal failure for the request. Both Failed and Fail
	// carry failure information: Failed is the boolean summary, Fail carries
	// structured HTTP status/body detail when available.
	Failed bool
	Fail   Failure
	// ChunkCount tracks the number of stream chunks observed for the request.
	ChunkCount int64
	// ResponseBytes / APIResponseBytes preserve the fork's byte counters.
	ResponseBytes    int64
	APIResponseBytes int64
	Detail           Detail
	// ResponseHeaders stores a snapshot of upstream response headers for usage sinks.
	ResponseHeaders http.Header
}

// Failure holds HTTP failure metadata for an upstream request attempt. It is
// populated when a request fails with a structured upstream error so plugin
// usage sinks can report status codes and response snippets.
type Failure struct {
	StatusCode int
	Body       string
}

// Detail holds the token usage breakdown.
type Detail struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
}

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}

// WithRequestedModelAlias stores the client-requested model name for usage sinks.
func WithRequestedModelAlias(ctx context.Context, alias string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ctx
	}
	return context.WithValue(ctx, requestedModelAliasContextKey{}, alias)
}

// RequestedModelAliasFromContext returns the client-requested model name stored in ctx.
func RequestedModelAliasFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(requestedModelAliasContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithReasoningEffort stores the client-requested reasoning effort for usage sinks.
func WithReasoningEffort(ctx context.Context, effort string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, reasoningEffortContextKey{}, effort)
}

// ReasoningEffortFromContext returns the client-requested reasoning effort stored in ctx.
func ReasoningEffortFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(reasoningEffortContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
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
	// named keeps the slice index of plugins registered via RegisterNamed so
	// that re-registering under the same name replaces the existing plugin in
	// place instead of appending duplicates. This is used by the plugin host
	// to refresh usage plugins without growing the delivery list unbounded.
	named map[string]int
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

// RegisterNamed registers or replaces a plugin by name. When name already
// exists the previously-registered plugin is replaced in place so the delivery
// order is preserved; otherwise the plugin is appended. Plugins registered by
// name can be refreshed (e.g. by the plugin host when reloading a plugin)
// without producing duplicates in the dispatch list.
func (m *Manager) RegisterNamed(name string, plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}

	m.pluginsMu.Lock()
	if m.named == nil {
		m.named = make(map[string]int)
	}
	if index, exists := m.named[name]; exists && index >= 0 && index < len(m.plugins) {
		m.plugins[index] = plugin
		m.pluginsMu.Unlock()
		return
	}
	m.named[name] = len(m.plugins)
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

// RegisterNamedPlugin registers or replaces a named plugin on the default
// manager. The plugin host uses this to keep the delivery list stable across
// plugin reloads.
func RegisterNamedPlugin(name string, plugin Plugin) { DefaultManager().RegisterNamed(name, plugin) }

// PublishRecord publishes a record using the default manager.
func PublishRecord(ctx context.Context, record Record) { DefaultManager().Publish(ctx, record) }

// StartDefault starts the default manager's dispatcher.
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault stops the default manager's dispatcher.
func StopDefault() { DefaultManager().Stop() }

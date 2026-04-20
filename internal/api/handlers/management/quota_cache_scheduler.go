package management

import (
	"context"
	"math/rand"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const quotaCacheRefreshBatchSize = 20

type quotaCacheScheduler struct {
	service *quotaCacheService

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	rand   *rand.Rand
}

func newQuotaCacheScheduler(service *quotaCacheService) *quotaCacheScheduler {
	return &quotaCacheScheduler{
		service: service,
		rand:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (s *quotaCacheScheduler) Start(parent context.Context) {
	if s == nil || s.service == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(ctx, s.done)
}

func (s *quotaCacheScheduler) Stop(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done == nil {
		return
	}
	if ctx == nil {
		<-done
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *quotaCacheScheduler) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	snapshot, existed, err := s.service.Load()
	if err != nil {
		log.WithError(err).Warn("load quota cache snapshot failed")
	}
	if !existed || len(snapshot.Entries) == 0 {
		s.refreshInBatches(ctx, false)
	}

	ticker := time.NewTicker(quotaCacheRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshInBatches(ctx, false)
		}
	}
}

func (s *quotaCacheScheduler) refreshInBatches(ctx context.Context, force bool) {
	if s == nil || s.service == nil {
		return
	}
	auths := s.service.listAuths()
	entryMap := quotaCacheEntryMap(s.service.Snapshot().Entries)
	targets := selectQuotaRefreshTargets(auths, entryMap, nil, force, time.Now().UTC())
	for start := 0; start < len(targets); start += quotaCacheRefreshBatchSize {
		end := start + quotaCacheRefreshBatchSize
		if end > len(targets) {
			end = len(targets)
		}
		indexes := make([]string, 0, end-start)
		for _, target := range targets[start:end] {
			indexes = append(indexes, target.AuthIndex)
		}
		if _, err := s.service.Refresh(ctx, quotaCacheRefreshRequest{AuthIndexes: indexes, Force: force}); err != nil {
			log.WithError(err).Warn("refresh quota cache batch failed")
		}
		if end >= len(targets) {
			continue
		}
		delay := 3*time.Second + time.Duration(s.rand.Intn(3))*time.Second
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (h *Handler) StartQuotaCacheScheduler(ctx context.Context) {
	if h == nil || h.quotaCacheScheduler == nil {
		return
	}
	h.quotaCacheScheduler.Start(ctx)
}

func (h *Handler) StopQuotaCacheScheduler(ctx context.Context) {
	if h == nil || h.quotaCacheScheduler == nil {
		return
	}
	h.quotaCacheScheduler.Stop(ctx)
}

package auth

import (
	"context"
	"testing"
	"time"
)

// TestHandleDueAuth_AdvancesPastNextWhenNotRefreshing 验证：
// 调度项已到期，但 shouldRefresh=false 时，不能继续以 next=now 入堆（会 CPU 空转）。
func TestHandleDueAuth_AdvancesPastNextWhenNotRefreshing(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()

	// Runtime evaluator: 永不刷新。nextRefreshCheckAt 对 evaluator 返回 now+interval，
	// 本身已安全。这里额外覆盖“逻辑上已到期却不刷新”的保护分支是否把 next 推到未来。
	auth := &Auth{
		ID:       "auth-no-refresh",
		Provider: "claude",
		Runtime:  neverRefreshEvaluator{},
	}
	m.mu.Lock()
	m.auths[auth.ID] = auth
	m.mu.Unlock()

	loop := newAuthAutoRefreshLoop(m, time.Second, 1)
	loop.upsert(auth.ID, now.Add(-time.Second))
	loop.handleDueAuth(context.Background(), now, auth.ID)

	next, ok := loop.peek()
	if !ok {
		t.Fatal("expected auth to remain scheduled with backoff")
	}
	if !next.After(now) {
		t.Fatalf("next must be in the future to avoid timer.Reset(0) spin, got %v now=%v", next, now)
	}
}

// TestResetTimer_EnforcesMinimumWait 确保 resetTimer 不会对已到期堆顶使用 0 等待。
func TestResetTimer_EnforcesMinimumWait(t *testing.T) {
	m := NewManager(nil, nil, nil)
	loop := newAuthAutoRefreshLoop(m, time.Second, 1)
	now := time.Now()
	loop.upsert("x", now.Add(-time.Second))

	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	var timerCh <-chan time.Time
	started := time.Now()
	loop.resetTimer(timer, &timerCh, now)
	if timerCh == nil {
		t.Fatal("expected timer channel")
	}
	select {
	case <-timerCh:
		elapsed := time.Since(started)
		if elapsed < 80*time.Millisecond {
			t.Fatalf("timer fired too early (%v); min wait should be ~100ms", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("timer did not fire")
	}
}

type neverRefreshEvaluator struct{}

func (neverRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return false }

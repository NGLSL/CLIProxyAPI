package access

import "testing"

func TestResultEnsureIndex_IsStable(t *testing.T) {
	result := &Result{Provider: "config-inline", Principal: "account-a"}
	got := result.EnsureIndex()
	if got == "" {
		t.Fatalf("EnsureIndex() = empty")
	}
	if got != StableIndex("config-inline", "account-a") {
		t.Fatalf("EnsureIndex() = %q, want %q", got, StableIndex("config-inline", "account-a"))
	}
	if gotAgain := result.EnsureIndex(); gotAgain != got {
		t.Fatalf("EnsureIndex() second call = %q, want %q", gotAgain, got)
	}
}

func TestStableIndex_UsesTrimmedIdentity(t *testing.T) {
	got := StableIndex(" config-inline ", " account-a ")
	want := StableIndex("config-inline", "account-a")
	if got == "" {
		t.Fatalf("StableIndex() = empty")
	}
	if got != want {
		t.Fatalf("StableIndex() = %q, want %q", got, want)
	}
}

func TestStableIndex_RejectsIncompleteIdentity(t *testing.T) {
	if got := StableIndex("", "account-a"); got != "" {
		t.Fatalf("StableIndex() with empty provider = %q, want empty", got)
	}
	if got := StableIndex("config-inline", ""); got != "" {
		t.Fatalf("StableIndex() with empty principal = %q, want empty", got)
	}
}

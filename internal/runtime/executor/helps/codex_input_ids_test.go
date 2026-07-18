package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeCodexInputItemIDs_ShortensOverlongAndIsDeterministic(t *testing.T) {
	longID := strings.Repeat("a", 80)
	body := []byte(`{"input":[{"type":"message","id":"` + longID + `","role":"user"},{"type":"message","id":"short","role":"assistant"}]}`)
	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)
	got := gjson.GetBytes(first, "input.0.id").String()
	if got == longID {
		t.Fatalf("expected overlong id to be shortened, still %q", got)
	}
	if len([]rune(got)) > codexInputItemIDLimit {
		t.Fatalf("shortened id length %d exceeds limit", len([]rune(got)))
	}
	if gjson.GetBytes(first, "input.1.id").String() != "short" {
		t.Fatalf("short id should stay unchanged")
	}
	if gjson.GetBytes(second, "input.0.id").String() != got {
		t.Fatalf("shortening must be deterministic")
	}
}

func TestSanitizeCodexInputItemIDs_AvoidsCollision(t *testing.T) {
	// Two different long IDs that share the same long prefix.
	base := strings.Repeat("b", 70)
	id1 := base + "1"
	id2 := base + "2"
	body := []byte(`{"input":[{"id":"` + id1 + `"},{"id":"` + id2 + `"}]}`)
	out := SanitizeCodexInputItemIDs(body)
	a := gjson.GetBytes(out, "input.0.id").String()
	b := gjson.GetBytes(out, "input.1.id").String()
	if a == "" || b == "" || a == b {
		t.Fatalf("expected distinct shortened ids, got %q and %q", a, b)
	}
}

package executor

import (
	"context"
	"sync"
	"testing"

	"github.com/NGLSL/CLIProxyAPI/v6/internal/runtime/executor/helps"
	coreusage "github.com/NGLSL/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestEnsureImageGenerationTool_NoTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
	if arr[0].Get("output_format").String() != "png" {
		t.Fatalf("expected output_format=png, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_ExistingToolsWithoutImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"get_weather","parameters":{}}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "function" {
		t.Fatalf("expected first tool type=function, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_AlreadyPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","output_format":"webp"},{"type":"function","name":"f1"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools (no duplicate), got %d", len(arr))
	}
	if arr[0].Get("output_format").String() != "webp" {
		t.Fatalf("expected original output_format=webp preserved, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_EmptyToolsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_WebSearchAndImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"web_search"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "web_search" {
		t.Fatalf("expected first tool type=web_search, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_GPT53CodexSparkDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex-spark","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.3-codex-spark", nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for gpt-5.3-codex-spark, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

type usageCapturePlugin struct {
	mu      sync.Mutex
	records []coreusage.Record
}

func (p *usageCapturePlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, record)
}

func (p *usageCapturePlugin) models() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	models := make([]string, 0, len(p.records))
	for _, record := range p.records {
		models = append(models, record.Model)
	}
	return models
}

func TestPublishCodexImageToolUsageSkipsToolUsageWithoutImageOutput(t *testing.T) {
	manager := coreusage.ResetDefaultManagerForTest(t)
	capture := &usageCapturePlugin{}
	manager.Register(capture)

	reporter := helps.NewUsageReporter(context.Background(), "codex", "gpt-5.4", nil)
	body := []byte(`{"tools":[{"type":"image_generation"}]}`)
	completed := []byte(`{"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"tool_usage":{"image_gen":{"images":1}}}}`)

	publishCodexImageToolUsage(context.Background(), reporter, body, completed)
	manager.Stop()

	if got := capture.models(); len(got) != 0 {
		t.Fatalf("published models = %v, want none", got)
	}
}

func TestPublishCodexImageToolUsagePublishesWhenImageOutputExists(t *testing.T) {
	manager := coreusage.ResetDefaultManagerForTest(t)
	capture := &usageCapturePlugin{}
	manager.Register(capture)

	reporter := helps.NewUsageReporter(context.Background(), "codex", "gpt-5.4", nil)
	body := []byte(`{"tools":[{"type":"image_generation","model":"custom-image-model"}]}`)
	completed := []byte(`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","result":"aGVsbG8="}],"tool_usage":{"image_gen":{"images":1,"total_tokens":9}}}}`)

	publishCodexImageToolUsage(context.Background(), reporter, body, completed)
	manager.Stop()

	got := capture.models()
	if len(got) != 2 {
		t.Fatalf("published models = %v, want main and image records", got)
	}
	if got[0] != "gpt-5.4" {
		t.Fatalf("main model = %q, want gpt-5.4", got[0])
	}
	if got[1] != "custom-image-model" {
		t.Fatalf("image model = %q, want custom-image-model", got[1])
	}
}

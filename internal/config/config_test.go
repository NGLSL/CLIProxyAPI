package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadConfigOptionalQuotaCacheRefreshInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    int
	}{
		{
			name: "default when field is absent",
			content: `host: "127.0.0.1"
port: 8317
`,
			want: DefaultQuotaCacheRefreshInterval,
		},
		{
			name: "custom positive value is kept",
			content: `quota-cache-refresh-interval: 120
`,
			want: 120,
		},
		{
			name: "zero falls back to default",
			content: `quota-cache-refresh-interval: 0
`,
			want: DefaultQuotaCacheRefreshInterval,
		},
		{
			name: "negative falls back to default",
			content: `quota-cache-refresh-interval: -30
`,
			want: DefaultQuotaCacheRefreshInterval,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			cfg, err := LoadConfigOptional(configPath, false)
			if err != nil {
				t.Fatalf("LoadConfigOptional() error = %v", err)
			}
			if cfg.QuotaCacheRefreshInterval != tt.want {
				t.Fatalf("QuotaCacheRefreshInterval = %d, want %d", cfg.QuotaCacheRefreshInterval, tt.want)
			}
		})
	}
}

func TestIsKnownDefaultValueRecognizesQuotaCacheRefreshInterval(t *testing.T) {
	t.Parallel()

	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "3600"}
	if !isKnownDefaultValue([]string{"quota-cache-refresh-interval"}, node) {
		t.Fatal("expected quota-cache-refresh-interval=3600 to be treated as known default")
	}

	nonDefaultNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "120"}
	if isKnownDefaultValue([]string{"quota-cache-refresh-interval"}, nonDefaultNode) {
		t.Fatal("expected quota-cache-refresh-interval=120 not to be treated as known default")
	}
}

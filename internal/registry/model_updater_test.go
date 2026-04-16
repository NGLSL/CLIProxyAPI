package registry

import "testing"

func TestEmbeddedModelsCatalogPassesStartupValidation(t *testing.T) {
	if len(embeddedModelsJSON) == 0 {
		t.Fatal("embedded models catalog is empty")
	}

	if err := loadModelsFromBytes(embeddedModelsJSON, "test"); err != nil {
		t.Fatalf("embedded models catalog should pass startup validation: %v", err)
	}
}

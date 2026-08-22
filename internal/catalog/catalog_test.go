package catalog

import "testing"

func TestFirstCatalogID(t *testing.T) {
	if Llama318BQ4 != "llama-3.1-8b-q4" {
		t.Fatalf("catalog id %q", Llama318BQ4)
	}
}

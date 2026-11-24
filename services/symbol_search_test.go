package services

import (
	"os"
	"testing"
)

func TestLocalSymbolSearchFindsCompanyName(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(".."); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	results := localSymbolResults("Dell")
	if len(results) == 0 {
		t.Fatal("expected Dell in local symbol search")
	}
	if results[0].Symbol != "DELL" {
		t.Fatalf("expected DELL, got %+v", results[0])
	}
	if results[0].Name == "" {
		t.Fatal("expected company name for DELL")
	}
}

func TestMergeSymbolResultsDeduplicates(t *testing.T) {
	got := mergeSymbolResults(
		[]SymbolSearchResult{{Symbol: "DELL", Name: "Dell", Source: "local"}},
		[]SymbolSearchResult{{Symbol: "DELL", Name: "Dell duplicate", Source: "yahoo"}, {Symbol: "AAPL", Name: "Apple", Source: "yahoo"}},
	)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique results, got %+v", got)
	}
	if got[0].Source != "local" || got[1].Symbol != "AAPL" {
		t.Fatalf("unexpected merge order: %+v", got)
	}
}

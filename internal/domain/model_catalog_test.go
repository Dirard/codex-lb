package domain

import "testing"

func TestBootstrapCatalogCoversSelectedSlugs(t *testing.T) {
	catalog := BootstrapCatalog()
	want := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4",
		"gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.2", "codex-auto-review"}
	if len(catalog) != len(want) {
		t.Fatalf("bootstrap catalog size = %d, want %d", len(catalog), len(want))
	}
	for _, slug := range want {
		if catalog[slug].Slug == "" {
			t.Fatalf("missing bootstrap model %s", slug)
		}
	}
	for _, slug := range []string{"gpt-5.6-sol", "gpt-5.6-terra"} {
		if !catalog[slug].ServiceTiers()["priority"] {
			t.Fatalf("%s does not advertise Fast", slug)
		}
	}
	if catalog["gpt-5.6-luna"].ServiceTiers()["ultrafast"] {
		t.Fatal("bootstrap invented ultrafast entitlement")
	}
	if catalog["codex-auto-review"].Visibility() != "hide" {
		t.Fatal("auto-review must stay hidden")
	}
}

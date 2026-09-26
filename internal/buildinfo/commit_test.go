package buildinfo

import "testing"

func TestCurrentUsesLdflags(t *testing.T) {
	prev := Commit
	t.Cleanup(func() { Commit = prev })
	Commit = "abc123deadbeef"
	if got := Current(); got != "abc123deadbeef" {
		t.Fatalf("Current %q", got)
	}
}

func TestCurrentVersionStripsV(t *testing.T) {
	prev := Version
	t.Cleanup(func() { Version = prev })
	Version = "v1.2.3"
	if got := CurrentVersion(); got != "1.2.3" {
		t.Fatalf("version %q", got)
	}
}

func TestCurrentFallbackIsNonEmpty(t *testing.T) {
	prev := Commit
	t.Cleanup(func() { Commit = prev })
	Commit = "  "
	got := Current()
	if got == "" {
		t.Fatal("fallback must not be empty")
	}
}

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

func TestCurrentBuild(t *testing.T) {
	prev := Build
	t.Cleanup(func() { Build = prev })
	Build = "12"
	if got := CurrentBuild(); got != 12 {
		t.Fatalf("build %d", got)
	}
	Build = ""
	if got := CurrentBuild(); got != 0 {
		t.Fatalf("empty build %d", got)
	}
	Build = "nope"
	if got := CurrentBuild(); got != 0 {
		t.Fatalf("invalid build %d", got)
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

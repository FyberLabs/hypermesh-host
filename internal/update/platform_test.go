package update

import (
	"context"
	"testing"
)

func TestNonLinuxCandidateCannotApplyLinuxUpdate(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "unknown"} {
		hooks := Hooks{ReadFile: func(string) ([]byte, error) { t.Fatal("non-Linux updater read installation files"); return nil, nil }, Install: func(string, []byte, string) (string, error) {
			t.Fatal("non-Linux updater installed Linux payload")
			return "", nil
		}}
		plan, err := tickForOS(goos, context.Background(), Config{Channel: "test"}, hooks)
		if err != nil || plan.Action != ActionOff {
			t.Fatal(plan, err)
		}
	}
}

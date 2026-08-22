package tunnel

import (
	"context"
	"errors"
	"testing"
)

func TestApplyEmptyIsNoop(t *testing.T) {
	if err := Apply(context.Background(), nil); err != nil {
		t.Fatalf("nil: %v", err)
	}
	if err := Apply(context.Background(), Payload{}); err != nil {
		t.Fatalf("empty: %v", err)
	}
}

func TestApplyPayloadErrors(t *testing.T) {
	err := Apply(context.Background(), Payload("wg-quick guessed"))
	if !errors.Is(err, ErrUnsupportedPayload) {
		t.Fatalf("got %v", err)
	}
}

func TestApplyHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Apply(ctx, nil); err == nil {
		t.Fatal("expected ctx error")
	}
}

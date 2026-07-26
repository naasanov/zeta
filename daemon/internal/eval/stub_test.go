package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

var errBoom = errors.New("boom")

func TestStubProvider_ReplaysInOrderThenCycles(t *testing.T) {
	p := NewStubProvider(StubResult{Output: "a"}, StubResult{Output: "b"})
	ctx := context.Background()

	for _, want := range []string{"a", "b", "a", "b"} {
		got, err := p.Complete(ctx, provider.Request{})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if got.Text != want {
			t.Fatalf("want %q, got %q", want, got.Text)
		}
	}
}

func TestStubProvider_ReturnsScriptedError(t *testing.T) {
	p := NewStubProvider(StubResult{Err: errBoom})
	_, err := p.Complete(context.Background(), provider.Request{})
	if !errors.Is(err, errBoom) {
		t.Fatalf("want scripted error, got %v", err)
	}
}

func TestStubProvider_EmptyScriptErrors(t *testing.T) {
	p := NewStubProvider()
	_, err := p.Complete(context.Background(), provider.Request{})
	if err == nil {
		t.Fatalf("want an error from an empty script")
	}
}

func TestStubProvider_RespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := NewStubProvider(StubResult{Output: "a"})
	_, err := p.Complete(ctx, provider.Request{})
	if err == nil {
		t.Fatalf("want an error from a canceled context")
	}
}

func TestStubProvider_NameModelDefaults(t *testing.T) {
	p := NewStubProvider()
	if p.Name() != "stub" || p.Model() != "stub-1" {
		t.Fatalf("want default name/model, got %s/%s", p.Name(), p.Model())
	}
	var zero StubProvider
	if zero.Name() != "stub" || zero.Model() != "stub-1" {
		t.Fatalf("want defaults even from zero value, got %s/%s", zero.Name(), zero.Model())
	}
}

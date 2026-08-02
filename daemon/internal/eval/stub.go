package eval

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/naasanov/zsh-autopilot/daemon/internal/provider"
)

// StubResult is one scripted outcome for StubProvider.
type StubResult struct {
	Output string
	Err    error
	TTFT   time.Duration
}

// StubProvider is a provider.Provider that returns scripted results in
// sequence, never touching the network. Backs both this package's unit
// tests and `cmd/eval -dry-run`, hence exported.
//
// The script replays cyclically (index i % len(script)): a single-element
// script drives "identical outputs" tests, a two-element alternating script
// drives "disagreement"/escalation tests, and StubResult{Err: ...} entries
// drive the errors-excluded-from-grading path.
type StubProvider struct {
	mu  sync.Mutex
	idx int

	Script []StubResult
	// PName/PModel back Name()/Model(); default to "stub"/"stub-1" when unset
	// so a zero-value StubProvider is still usable.
	PName  string
	PModel string
}

// NewStubProvider returns a StubProvider that replays script in order,
// cycling once it runs out.
func NewStubProvider(script ...StubResult) *StubProvider {
	return &StubProvider{Script: script, PName: "stub", PModel: "stub-1"}
}

func (s *StubProvider) Complete(ctx context.Context, _ provider.Request) (provider.Completion, error) {
	if err := ctx.Err(); err != nil {
		return provider.Completion{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.Script) == 0 {
		return provider.Completion{}, errors.New("eval: stub provider has no scripted results")
	}
	r := s.Script[s.idx%len(s.Script)]
	s.idx++
	if r.Err != nil {
		return provider.Completion{}, r.Err
	}
	return provider.Completion{Text: r.Output, TTFT: r.TTFT}, nil
}

func (s *StubProvider) Name() string {
	if s.PName == "" {
		return "stub"
	}
	return s.PName
}

func (s *StubProvider) Model() string {
	if s.PModel == "" {
		return "stub-1"
	}
	return s.PModel
}

func (s *StubProvider) RenderPrompt(req provider.Request) string {
	return provider.RenderChatPrompt(req)
}

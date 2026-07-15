package provider

import (
	"testing"
	"time"
)

func TestAccumulator_NoNewline(t *testing.T) {
	a := newAccumulator(time.Now())
	if stop := a.Push("git "); stop {
		t.Fatalf("Push() stop = true, want false")
	}
	if stop := a.Push("status"); stop {
		t.Fatalf("Push() stop = true, want false")
	}
	if got, want := a.Text(), "git status"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestAccumulator_CutoffMidDelta(t *testing.T) {
	a := newAccumulator(time.Now())
	a.Push("foo")
	stop := a.Push(" bar\nbaz-should-not-appear")
	if !stop {
		t.Fatalf("Push() stop = false, want true")
	}
	if got, want := a.Text(), "foo bar"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestAccumulator_CutoffOnExactNewlineDelta(t *testing.T) {
	a := newAccumulator(time.Now())
	a.Push("foo bar")
	stop := a.Push("\n")
	if !stop {
		t.Fatalf("Push() stop = false, want true")
	}
	if got, want := a.Text(), "foo bar"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestAccumulator_EmptyDeltasDoNotStampTTFT(t *testing.T) {
	a := newAccumulator(time.Now())
	a.Push("")
	a.Push("")
	if got := a.TTFT(); got != 0 {
		t.Errorf("TTFT() = %v, want 0 after only empty deltas", got)
	}
}

func TestAccumulator_TTFTStampedOnFirstNonEmptyDelta(t *testing.T) {
	start := time.Now()
	a := newAccumulator(start)
	time.Sleep(5 * time.Millisecond)
	a.Push("")
	a.Push("git")
	if got := a.TTFT(); got <= 0 {
		t.Errorf("TTFT() = %v, want > 0", got)
	}

	// A second non-empty delta must not re-stamp TTFT.
	ttft1 := a.TTFT()
	time.Sleep(5 * time.Millisecond)
	a.Push(" status")
	if got := a.TTFT(); got != ttft1 {
		t.Errorf("TTFT() changed after a second delta: got %v, want unchanged %v", got, ttft1)
	}
}

func TestAccumulator_IdempotentStop(t *testing.T) {
	a := newAccumulator(time.Now())
	a.Push("foo\n")
	if got, want := a.Text(), "foo"; got != want {
		t.Fatalf("Text() = %q, want %q", got, want)
	}
	// Further pushes after stop are no-ops that keep returning stop=true and
	// must not mutate the accumulated text.
	stop := a.Push("more-should-not-appear")
	if !stop {
		t.Errorf("Push() after stop returned stop = false, want true")
	}
	if got, want := a.Text(), "foo"; got != want {
		t.Errorf("Text() after stop = %q, want %q (unchanged)", got, want)
	}
}

func TestAccumulator_NoNewlineTextIsFullAccumulation(t *testing.T) {
	a := newAccumulator(time.Now())
	a.Push("git")
	a.Push(" commit")
	if got, want := a.Text(), "git commit"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

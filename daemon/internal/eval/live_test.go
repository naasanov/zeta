package eval

import (
	"os"
	"strings"
	"testing"
)

// clearKeyEnv unsets every key env this test suite touches, restoring the
// prior values on Cleanup, so these tests are safe to run in an environment
// that (accidentally or otherwise) has real provider keys exported — a live
// key present in the test process's env must never turn a "missing key
// errors clearly" test into an accidental network call.
func clearKeyEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		old, had := os.LookupEnv(name)
		os.Unsetenv(name)
		t.Cleanup(func() {
			if had {
				os.Setenv(name, old)
			}
		})
	}
}

func TestNewLiveProvider_UnknownBrand(t *testing.T) {
	_, err := NewLiveProvider("not-a-real-brand", "", 48, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown brand")
	}
}

func TestNewLiveProvider_MissingKey(t *testing.T) {
	clearKeyEnv(t, "ZSH_AUTOPILOT_CODESTRAL_KEY")

	_, err := NewLiveProvider("codestral", "", 48, nil)
	if err == nil {
		t.Fatal("expected an error when ZSH_AUTOPILOT_CODESTRAL_KEY is unset")
	}
	if !strings.Contains(err.Error(), "ZSH_AUTOPILOT_CODESTRAL_KEY") {
		t.Errorf("error %q does not name the missing env var", err.Error())
	}
}

func TestNewLiveProvider_MissingKey_Anthropic(t *testing.T) {
	clearKeyEnv(t, "ZSH_AUTOPILOT_ANTHROPIC_KEY")

	_, err := NewLiveProvider("anthropic", "", 48, nil)
	if err == nil {
		t.Fatal("expected an error when ZSH_AUTOPILOT_ANTHROPIC_KEY is unset")
	}
	if !strings.Contains(err.Error(), "ZSH_AUTOPILOT_ANTHROPIC_KEY") {
		t.Errorf("error %q does not name the missing env var", err.Error())
	}
}

func TestNewLiveProvider_MissingKey_Groq(t *testing.T) {
	clearKeyEnv(t, "ZSH_AUTOPILOT_GROQ_KEY")

	_, err := NewLiveProvider("groq", "", 48, nil)
	if err == nil {
		t.Fatal("expected an error when ZSH_AUTOPILOT_GROQ_KEY is unset")
	}
	if !strings.Contains(err.Error(), "ZSH_AUTOPILOT_GROQ_KEY") {
		t.Errorf("error %q does not name the missing env var", err.Error())
	}
}

// TestNewLiveProvider_OllamaNeedsNoKey constructs the ollama-brand provider
// with no key set at all. This does NOT hit the network — provider
// constructors (NewOpenAI/NewAnthropic/NewCodestral) only build an
// http.Client and store config; they make no request until Complete is
// called, which this test never does.
func TestNewLiveProvider_OllamaNeedsNoKey(t *testing.T) {
	p, err := NewLiveProvider("ollama", "", 48, nil)
	if err != nil {
		t.Fatalf("ollama should construct without any key: %v", err)
	}
	if p == nil {
		t.Fatal("expected a non-nil provider")
	}
	if p.Name() != "openai" {
		t.Errorf("ollama brand should use the openai adapter internally, got Name() = %q", p.Name())
	}
}

// TestNewLiveProvider_ModelOverride confirms -model's override reaches the
// constructed provider's resolved Model(), the plan doc's "pin the model per
// run, record it" rule.
func TestNewLiveProvider_ModelOverride(t *testing.T) {
	p, err := NewLiveProvider("ollama", "some-pinned-model:latest", 48, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Model() != "some-pinned-model:latest" {
		t.Errorf("Model() = %q, want the override to have applied", p.Model())
	}
}

func TestNewLiveProvider_NoOverrideKeepsPresetModel(t *testing.T) {
	p, err := NewLiveProvider("ollama", "", 48, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Model() == "" {
		t.Error("Model() should default to the preset's model when no override is given")
	}
}

func TestLimiterForBrand(t *testing.T) {
	if _, ok := LimiterForBrand("groq").(*RateLimiter); !ok {
		t.Errorf("LimiterForBrand(%q) = %T, want *RateLimiter", "groq", LimiterForBrand("groq"))
	}
	for _, brand := range []string{"codestral", "anthropic", "ollama", "unknown-brand"} {
		if _, ok := LimiterForBrand(brand).(NoopLimiter); !ok {
			t.Errorf("LimiterForBrand(%q) = %T, want NoopLimiter", brand, LimiterForBrand(brand))
		}
	}
}

package provider

import (
	"strings"
	"testing"

	"github.com/naasanov/zsh-autopilot/daemon/internal/prompt"
)

func TestRenderChatPrompt(t *testing.T) {
	req := Request{Prompt: prompt.Prompt{
		System:      "sys text",
		Instruction: "do the thing: ",
		Context:     "Context:\n- cwd: /x\n\n",
		Prefix:      "git sta",
	}}
	got := RenderChatPrompt(req)

	if !strings.Contains(got, "SYSTEM:\nsys text") {
		t.Errorf("RenderChatPrompt() = %q, want it to contain the rendered system block", got)
	}
	wantUser := req.Prompt.ChatUser()
	if !strings.Contains(got, "USER:\n"+wantUser) {
		t.Errorf("RenderChatPrompt() = %q, want it to contain USER:\\n%q", got, wantUser)
	}
}

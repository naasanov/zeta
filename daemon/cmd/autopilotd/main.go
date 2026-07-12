// Command autopilotd is the zsh-autopilot daemon: a long-running process that
// the zsh client talks to over a Unix domain socket. It holds warm keep-alive
// connections to LLM providers, debounces keystroke-driven requests, cancels
// stale in-flight requests, and streams suggestions back to the shell.
//
// See .docs/zeta_design_doc_v3.md §3 for the architecture.
package main

func main() {
	// TODO: implement.
}

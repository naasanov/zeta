# zsh-autopilot

Automatic inline AI autosuggestions for zsh — cloud/general-LLM **ghost text
that appears automatically as you type, no hotkey, inside your existing zsh.**

Think [zsh-autosuggestions](https://github.com/zsh-users/zsh-autosuggestions)'
grey ghost-text UX, but the suggestions come from an LLM instead of your
history. See [.docs/zeta_design_doc_v3.md](.docs/zeta_design_doc_v3.md) for the
full design and execution plan.

## Architecture

Two pieces talk over a Unix domain socket:

- **A thin zsh client** ([`zsh/`](zsh/)) — ZLE widgets, lifecycle hooks, and
  ghost-text rendering. The genuinely fragile, novel part.
- **A long-running Go daemon** ([`daemon/`](daemon/)) — holds warm keep-alive
  connections to LLM providers, debounces keystrokes, cancels stale in-flight
  requests, caches context, and streams back single-line suggestions.

Keeping the network/LLM work in a persistent daemon (instead of forking
`curl` per keystroke) is what makes automatic-on-keystroke latency feel okay.

## Layout

```
zsh-autopilot.plugin.zsh   Plugin entry point (sourced by plugin managers)
zsh-autopilot.zsh          The ZLE client — GENERATED bundle, do not edit
zsh/                       Numbered client fragments (10_config … 60_start)
daemon/                    The Go daemon (its own module)
  cmd/autopilotd/            main()
spike/echo-server/         Phase 0 fake backend to de-risk the zsh primitives
```

The zsh client is implemented (Phase 0). The top-level `zsh-autopilot.zsh` is
built from `zsh/*.zsh` by `make plugin` (or the pre-commit hook) — edit the
fragments in `zsh/`, not the bundle. The Go daemon is still a Phase 1 stub.

## Status

**Phase 0 — complete.** The risky zsh primitives work end-to-end against the
throwaway [`spike/echo-server`](spike/echo-server): ghost text via POSTDISPLAY, a
full async `zle -F` round-trip without blocking the prompt, accept/clear, and a
next-command suggestion fired from `precmd`. No real LLM or daemon yet — that's
Phase 1.

## Development

```sh
make           # build the daemon + zsh bundle
make plugin    # rebuild the zsh bundle from zsh/*.zsh -> zsh-autopilot.zsh
make spike     # build the Phase 0 echo server        -> bin/echo-server
make daemon    # build the daemon                     -> bin/autopilotd
make hooks     # install the pre-commit hook (regenerates the bundle on commit)
```

## License

MIT — see [LICENSE](LICENSE). Portions adapted from zsh-autosuggestions (MIT).

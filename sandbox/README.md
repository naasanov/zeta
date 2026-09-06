# sandbox

Isolated environment for developing the zsh-autopilot ZLE client without your
real shell config (oh-my-zsh, zsh-autosuggestions, etc.) interfering.

## Why

The plugin paints grey ghost text via `POSTDISPLAY`/`region_highlight` — the
exact mechanism `zsh-autosuggestions` uses. Running both at once conflicts. This
sandbox launches a clean zsh where **only** our plugin loads, by pointing
`ZDOTDIR` at this folder so none of your `~/.zsh*` files are read.

## Use

From VSCode: **Run and Debug → `sandbox`**, a compound that opens the sandbox
shell plus a second terminal tailing the daemon log. `sandbox: fresh zsh` on its
own skips the log terminal. Either way the daemon is rebuilt first.

From a terminal, at the repo root:

```sh
ZDOTDIR=sandbox zsh
```

Type `exit` to return to your normal shell.

The shell lazy-spawns the locally built daemon (`bin/autopilotd`) on its first
request, so nothing else needs to be running. Launching from VSCode rebuilds it
and kills the previous one first; from a terminal, run `make` yourself.

## Dev cycle

1. Edit a client fragment in `zsh/NN_*.zsh`, or the daemon under `daemon/`.
2. `make` to rebuild the bundle and the daemon binary.
3. Reload: `exit` the sandbox and re-run the task / command for a clean shell.
   (Fresh shell each time avoids stale ZLE widget & hook registrations.)
4. Observe. Daemon logs land in `sandbox/.state/autopilot/daemon.log`, which the
   `sandbox` compound tails for you.

## Which plugin loads

Defaults to the real entry point (`../zsh-autopilot.plugin.zsh`), which sources
the generated bundle `../zsh-autopilot.zsh`. Override to load a different file:

```sh
AUTOPILOT_PLUGIN=/path/to/other.plugin.zsh ZDOTDIR=sandbox zsh
```

## Files

- `.zshrc` — isolated config. Derives every sandbox path from its own location
  and exports the defaults (dev socket, daemon binary, metrics, state dir),
  then sources `.env` on top.
- `.env` — your secrets and overrides only, as `export` lines (gitignored).
  Copy `.env.example` to start. **Required**: the sandbox unsets every inherited
  `ZSH_AUTOPILOT_*`, so a key exported by your real `~/.zshrc` does not reach it.
- `.zsh_history`, `.state/`, `.metrics/`, `.history/` — sandbox-local runtime
  state (all gitignored).

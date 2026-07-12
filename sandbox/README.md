# sandbox

Isolated environment for developing the zsh-autopilot ZLE client without your
real shell config (oh-my-zsh, zsh-autosuggestions, etc.) interfering.

## Why

The plugin paints grey ghost text via `POSTDISPLAY`/`region_highlight` — the
exact mechanism `zsh-autosuggestions` uses. Running both at once conflicts. This
sandbox launches a clean zsh where **only** our plugin loads, by pointing
`ZDOTDIR` at this folder so none of your `~/.zsh*` files are read.

## Use

From VSCode: **Run Task → `sandbox: fresh zsh`** (Cmd/Ctrl-Shift-P → "Run Task").

From a terminal, at the repo root:

```sh
ZDOTDIR=sandbox zsh
```

Type `exit` to return to your normal shell.

## Dev cycle

1. Edit the client: `zsh/zsh-autopilot.zsh`.
2. Reload: `exit` the sandbox and re-run the task / command for a clean shell.
   (Fresh shell each time avoids stale ZLE widget & hook registrations.)
3. Observe.

## Which plugin loads

Defaults to the real entry point (`../zsh-autopilot.plugin.zsh`), which sources
`zsh/zsh-autopilot.zsh`. Override to load a different file:

```sh
AUTOPILOT_PLUGIN=/path/to/other.plugin.zsh ZDOTDIR=sandbox zsh
```

## Files

- `.zshrc` — minimal isolated config; sets `$AUTOPILOT_PLUGIN` and sources it.
- `.zsh_history` — sandbox-local history (gitignored).

# sandbox/.zshrc — isolated zsh config for developing the zsh-autopilot client.
#
# This runs when zsh is launched with ZDOTDIR pointing at this folder, e.g. the
# "sandbox: fresh zsh" VSCode task, or from a shell:
#
#     ZDOTDIR=sandbox zsh
#
# Because ZDOTDIR redirects where zsh looks for user rc files, NONE of your real
# ~/.zshrc, oh-my-zsh, or zsh-autosuggestions loads here. Nothing else fights
# the plugin for POSTDISPLAY / ghost text, so what you see is purely our code.
# Type `exit` to drop back to your normal shell, fully intact.

# :A resolves to an absolute path, so everything below works from any cwd. The
# $0 fallback covers sourcing this file directly, where zsh sets no ZDOTDIR.
sandbox="${${ZDOTDIR:-${0:h}}:A}"
repo="${sandbox:h}"

# Keep sandbox history out of your real ~/.zsh_history.
HISTFILE="$sandbox/.zsh_history"
HISTSIZE=1000
SAVEHIST=1000

# ZDOTDIR redirects rc files but not the inherited environment, and a real
# ~/.zshrc commonly exports keys. Drop them so sandbox/.env is the only source
# and the sandbox can reproduce missing-key and bad-key failures.
unset -m 'ZSH_AUTOPILOT_*'

# Sandbox defaults. sandbox/.env is sourced after these and wins, so it only
# needs secrets and deliberate overrides.

# Talk to the locally built daemon, not an installed one on $PATH. The client
# lazy-spawns it on the first request, so nothing else has to be running.
export ZSH_AUTOPILOT_DAEMON_BIN="$repo/bin/autopilotd"

# Distinct sockets from an installed instance, whose single-instance guard is
# per-socket, so both can run at once.
export ZSH_AUTOPILOT_SOCKET=/tmp/zsh-autopilot-dev.sock
export ZSH_AUTOPILOT_METRICS_SOCKET=/tmp/zsh-autopilot-dev-metrics.sock

# Sandbox-local state, all gitignored. XDG_STATE_HOME is where the lazy-spawned
# daemon writes autopilot/daemon.log.
export XDG_STATE_HOME="$sandbox/.state"
export ZSH_AUTOPILOT_METRICS=1
export ZSH_AUTOPILOT_METRICS_LOG="$sandbox/.metrics/events.jsonl"
export ZSH_AUTOPILOT_HISTORY_JOURNAL="$sandbox/.history/history.jsonl"

# Debug-level daemon logging, and never let the self-updater reinstall or
# pkill the installed instance from in here.
export ZSH_AUTOPILOT_DEBUG=1
export ZSH_AUTOPILOT_AUTOUPDATE=0

[[ -r "$sandbox/config.toml" ]] && export ZSH_AUTOPILOT_CONFIG="$sandbox/config.toml"

# Developer secrets and overrides. Every line in it is a real `export`, so
# sourcing is all it takes. See sandbox/.env.example.
[[ -r "$sandbox/.env" ]] && source "$sandbox/.env"

# Make it unmistakable that you're in the sandbox.
PROMPT='%F{cyan}[autopilot-sandbox]%f %1~ %# '

# Which plugin to load. Defaults to the real client entry point. Override with
# AUTOPILOT_PLUGIN to point at a different file.
: "${AUTOPILOT_PLUGIN:=$repo/zsh-autopilot.plugin.zsh}"

if [[ -r "$AUTOPILOT_PLUGIN" ]]; then
  source "$AUTOPILOT_PLUGIN"
else
  print -u2 "sandbox: plugin not found: $AUTOPILOT_PLUGIN"
fi

unset sandbox repo

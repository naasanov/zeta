# sandbox/.zshrc — isolated zsh config for developing zsh-autopilot.
# Launch with: ZDOTDIR=sandbox zsh
# ZDOTDIR redirects rc-file lookup, so none of your real ~/.zshrc loads here.

# :A resolves to an absolute path; the ${0:h} fallback covers sourcing this
# file directly, when zsh sets no ZDOTDIR.
sandbox="${${ZDOTDIR:-${0:h}}:A}"
repo="${sandbox:h}"

HISTFILE="$sandbox/.zsh_history"
HISTSIZE=1000
SAVEHIST=1000

# ZDOTDIR isolates rc-file lookup, not the inherited environment; a real
# ~/.zshrc may still export keys. Unset them so sandbox/.env is the only
# source, and missing/bad-key failures reproduce reliably.
unset -m 'ZSH_AUTOPILOT_*'

# Sandbox defaults. sandbox/.env is sourced after these and wins, so it only
# needs secrets and deliberate overrides.

# Points at the locally built daemon, not one on $PATH; lazy-spawned on the
# first request.
export ZSH_AUTOPILOT_DAEMON_BIN="$repo/bin/autopilotd"

# Distinct sockets from an installed instance: the single-instance guard is
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

# Developer secrets and overrides; every line in it is a real `export`.
[[ -r "$sandbox/.env" ]] && source "$sandbox/.env"

PROMPT='%F{cyan}[autopilot-sandbox]%f %1~ %# '

: "${AUTOPILOT_PLUGIN:=$repo/zsh-autopilot.plugin.zsh}"

if [[ -r "$AUTOPILOT_PLUGIN" ]]; then
  source "$AUTOPILOT_PLUGIN"
else
  print -u2 "sandbox: plugin not found: $AUTOPILOT_PLUGIN"
fi

unset sandbox repo

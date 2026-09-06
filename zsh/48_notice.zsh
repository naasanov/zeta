
#--------------------------------------------------------------------#
# Notice Channel                                                     #
#--------------------------------------------------------------------#
# One-line diagnostics for non-recoverable failures (bad key, missing
# daemon, etc), queued by producers running outside widget context and
# printed once per shell per failure class from a precmd hook.

typeset -gA _ZSH_AUTOPILOT_SEEN_NOTICES
typeset -ga _ZSH_AUTOPILOT_PENDING_NOTICES

# true unless explicitly disabled with ZSH_AUTOPILOT_NOTICES=0 (or =false).
_zsh_autopilot_notices_enabled() {
  [[ $ZSH_AUTOPILOT_NOTICES != 0 && $ZSH_AUTOPILOT_NOTICES != false ]]
}

# Queue a notice for kind $1 with text $2, deduped per kind per shell.
# Never prints; safe to call from a widget or a `zle -F` fd callback,
# neither of which may write to stdout.
_zsh_autopilot_notice() {
  emulate -L zsh
  local kind="$1" text="$2"

  _zsh_autopilot_notices_enabled || return
  [[ -z $kind ]] && return
  [[ -n ${_ZSH_AUTOPILOT_SEEN_NOTICES[$kind]} ]] && return

  _ZSH_AUTOPILOT_SEEN_NOTICES[$kind]=1
  _ZSH_AUTOPILOT_PENDING_NOTICES+=("$text")
}

# precmd hook: print and clear any queued notices. Checked first since this
# runs on every prompt and the common case is an empty array.
_zsh_autopilot_drain_notices() {
  (( ${#_ZSH_AUTOPILOT_PENDING_NOTICES} == 0 )) && return

  local msg
  for msg in "${_ZSH_AUTOPILOT_PENDING_NOTICES[@]}"; do
    print -u2 -r -- "zsh-autopilot: $msg"
  done
  _ZSH_AUTOPILOT_PENDING_NOTICES=()
}

autoload -Uz add-zsh-hook
add-zsh-hook precmd _zsh_autopilot_drain_notices

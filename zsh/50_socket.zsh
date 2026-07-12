#--------------------------------------------------------------------#
# Daemon Socket Transport                                            #
#--------------------------------------------------------------------#
# Talks to autopilotd over the persistent Unix-domain socket: opens the
# warm connection, sends the current buffer/context on each modify, and
# registers a `zle -F` handler that paints the reply as ghost text.
# Replaces zsh-autosuggestions' async.zsh (forked-pipe) model.
#

_zsh_autopilot_connect() {
  zmodload zsh/net/socket 2>/dev/null || return 1

  # Drop any stale fd before opening a new one.
  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && exec {ZSH_AUTOPILOT_SOCKET_FD}<&- 2>/dev/null

  if ! zsocket $ZSH_AUTOPILOT_SOCKET 2>/dev/null; then
    unset ZSH_AUTOPILOT_SOCKET_FD
    return 1 # daemon not up - caller degrades gracefully
  fi
  typeset -g ZSH_AUTOPILOT_SOCKET_FD=$REPLY

  zle -F $ZSH_AUTOPILOT_SOCKET_FD _zsh_autopilot_receive
}

# true if $ZSH_AUTOPILOT_SOCKET_FD is a currently-open fd
_zsh_autopilot_socket_alive() {
  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && { true <&$ZSH_AUTOPILOT_SOCKET_FD } 2>/dev/null
}

_zsh_autopilot_send() {
  local buffer="$1"

  _zsh_autopilot_socket_alive || _zsh_autopilot_connect || return 1

  # Write; if the peer had gone away (half-open), reconnect once and retry.
  if ! print -r -u $ZSH_AUTOPILOT_SOCKET_FD -- "$buffer" 2>/dev/null; then
    _zsh_autopilot_connect || return 1
    print -r -u $ZSH_AUTOPILOT_SOCKET_FD -- "$buffer" 2>/dev/null || return 1
  fi
}

# precmd hook: at a fresh, empty prompt, ask the daemon what to run next. We
# signal "next command" by sending an empty buffer; the reply is painted on the
# empty line by the zle -F handler once the editor becomes active.
_zsh_autopilot_precmd() {
  _zsh_autopilot_send ''
}

# zle -F callback: fires while the line editor is active whenever the socket
# fd is readable (or errors). $1 = the fd; $2 = an error condition ("hup",
# "err", "nval") or empty on normal, readable data.
_zsh_autopilot_receive() {
  emulate -L zsh
  local fd=$1

  # Connection error or peer hangup: tear down so the next send reconnects.
  if [[ -n "$2" ]]; then
    zle -F $fd                # deregister this handler
    exec {fd}<&- 2>/dev/null   # close our end
    [[ $fd == $ZSH_AUTOPILOT_SOCKET_FD ]] && unset ZSH_AUTOPILOT_SOCKET_FD
    return
  fi

  # Normal path: read one newline-framed reply and paint it as ghost text.
  # The handler stays registered (persistent warm socket)
  local suggestion
  IFS= read -r -u $fd suggestion || return
  zle autopilot-suggest -- "$suggestion"
}

#--------------------------------------------------------------------#
# Daemon Socket Transport                                            #
#--------------------------------------------------------------------#
# Talks to autopilotd over the persistent Unix-domain socket: opens the
# warm connection, sends the current buffer/context on each modify, and
# registers a `zle -F` handler that paints the reply as ghost text.
# Replaces zsh-autosuggestions' async.zsh (forked-pipe) model.
#

# Per-shell identity for request IDs. The session id is minted once; each fetch
# bumps a sequence counter, and the id we most recently sent is the "current"
# request. Replies whose id != current are stale (the user typed on) and are
# dropped — this is the supersede-by-request-ID contract (protocol package doc).
typeset -g ZSH_AUTOPILOT_SESSION_ID=${ZSH_AUTOPILOT_SESSION_ID:-$$-$RANDOM}
typeset -gi _ZSH_AUTOPILOT_SEQ=0
typeset -g _ZSH_AUTOPILOT_REQ_ID=

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

# Send a request to the daemon. $1 = buffer, $2 = kind (typing|next_command).
# Mints a fresh request id, records it as current, and ships one JSON line.
_zsh_autopilot_send() {
  local buffer="$1" kind="${2:-typing}"

  _zsh_autopilot_socket_alive || _zsh_autopilot_connect || return 1

  (( _ZSH_AUTOPILOT_SEQ++ ))
  typeset -g _ZSH_AUTOPILOT_REQ_ID="${ZSH_AUTOPILOT_SESSION_ID}.${_ZSH_AUTOPILOT_SEQ}"

  local REPLY
  _zsh_autopilot_json_escape "$buffer"
  local line='{"v":1,"id":"'${_ZSH_AUTOPILOT_REQ_ID}'","kind":"'${kind}'","buf":"'${REPLY}'"}'

  # Write; if the peer had gone away (half-open), reconnect once and retry.
  if ! print -r -u $ZSH_AUTOPILOT_SOCKET_FD -- "$line" 2>/dev/null; then
    _zsh_autopilot_connect || return 1
    print -r -u $ZSH_AUTOPILOT_SOCKET_FD -- "$line" 2>/dev/null || return 1
  fi
}

# precmd hook: at a fresh, empty prompt, ask the daemon what to run next. The
# reply is painted on the empty line by the zle -F handler once the editor
# becomes active.
_zsh_autopilot_precmd() {
  _zsh_autopilot_send '' next_command
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

  # Normal path: read one newline-framed JSON reply. The handler stays
  # registered (persistent warm socket).
  local line
  IFS= read -r -u $fd line || return

  # Correlate by id: ignore replies for a request we've already superseded.
  local REPLY
  _zsh_autopilot_json_str_field "$line" id || return
  [[ $REPLY == $_ZSH_AUTOPILOT_REQ_ID ]] || return
  local reply_source
  local suggestion
  _zsh_autopilot_json_str_field "$line" source && reply_source=$REPLY
  _zsh_autopilot_json_str_field "$line" suggestion && suggestion=$REPLY || return

  zle autopilot-suggest -- "$reply_source" "$suggestion"
}

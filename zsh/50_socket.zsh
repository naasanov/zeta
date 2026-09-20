#--------------------------------------------------------------------#
# Daemon Socket Transport                                            #
#--------------------------------------------------------------------#

# Talks to autopilotd over the persistent Unix-domain socket, painting
# replies as ghost text via a zle -F handler.

# The most recently sent request id is current; a reply with a different id
# is stale and dropped.
typeset -g ZSH_AUTOPILOT_SESSION_ID=${ZSH_AUTOPILOT_SESSION_ID:-$$-$RANDOM}
typeset -gi _ZSH_AUTOPILOT_SEQ=0
typeset -g _ZSH_AUTOPILOT_REQ_ID=

zmodload zsh/datetime 2>/dev/null

# Seconds a shell tolerates an absent daemon before reporting one, when
# autostart is off.
typeset -gi _ZSH_AUTOPILOT_CONNECT_GRACE=10
typeset -gi _ZSH_AUTOPILOT_FIRST_CONNECT_FAIL=0

_zsh_autopilot_notice_after_grace() {
  if (( ! _ZSH_AUTOPILOT_FIRST_CONNECT_FAIL )); then
    _ZSH_AUTOPILOT_FIRST_CONNECT_FAIL=$EPOCHSECONDS
    return
  fi
  (( EPOCHSECONDS - _ZSH_AUTOPILOT_FIRST_CONNECT_FAIL >= _ZSH_AUTOPILOT_CONNECT_GRACE )) &&
    _zsh_autopilot_notice "$1" "$2"
  return 0
}

_zsh_autopilot_daemon_bin_present() {
  [[ -n $ZSH_AUTOPILOT_DAEMON_BIN ]] || return 1
  if [[ $ZSH_AUTOPILOT_DAEMON_BIN == */* ]]; then
    [[ -x $ZSH_AUTOPILOT_DAEMON_BIN ]]
  else
    (( $+commands[$ZSH_AUTOPILOT_DAEMON_BIN] ))
  fi
}

_zsh_autopilot_spawn_daemon() {
  (( _ZSH_AUTOPILOT_SPAWN_TRIED )) && return 1
  typeset -g _ZSH_AUTOPILOT_SPAWN_TRIED=1

  _zsh_autopilot_daemon_bin_present || return 1

  local log_dir="${XDG_STATE_HOME:-$HOME/.local/state}/autopilot"
  mkdir -p "$log_dir" 2>/dev/null

  # zsh does not export $HISTFILE; it is passed explicitly here.
  ( ZSH_AUTOPILOT_HISTFILE=$HISTFILE nohup "$ZSH_AUTOPILOT_DAEMON_BIN" -socket "$ZSH_AUTOPILOT_SOCKET" >>"$log_dir/daemon.log" 2>&1 & )
}

_zsh_autopilot_connect() {
  zmodload zsh/net/socket 2>/dev/null || return 1

  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && exec {ZSH_AUTOPILOT_SOCKET_FD}<&- 2>/dev/null

  if ! zsocket $ZSH_AUTOPILOT_SOCKET 2>/dev/null; then
    unset ZSH_AUTOPILOT_SOCKET_FD

    # Checked directly, not from spawn_daemon's return code: that code is 1
    # for both the once-per-shell guard and a missing binary.
    local -i bin_present=0
    _zsh_autopilot_daemon_bin_present && bin_present=1

    if (( ! bin_present )); then
      if [[ -n $ZSH_AUTOPILOT_DAEMON_BIN ]]; then
        # A missing/unrunnable named binary is a permanent, broken install:
        # report immediately, no grace.
        _zsh_autopilot_notice no_daemon_bin \
          "daemon binary '$ZSH_AUTOPILOT_DAEMON_BIN' not found or not executable; reinstall zsh-autopilot or fix \$ZSH_AUTOPILOT_DAEMON_BIN"
        return 1
      fi
      # Empty means autostart is off; someone else may own the daemon's lifecycle.
      _zsh_autopilot_notice_after_grace no_daemon \
        "nothing listening on $ZSH_AUTOPILOT_SOCKET and autostart is off (ZSH_AUTOPILOT_DAEMON_BIN is empty); start autopilotd yourself"
      return 1
    fi

    if _zsh_autopilot_spawn_daemon; then
      local -i tries=0
      while (( tries++ < 10 )); do
        if zsocket $ZSH_AUTOPILOT_SOCKET 2>/dev/null; then
          typeset -g ZSH_AUTOPILOT_SOCKET_FD=$REPLY
          zle -F $ZSH_AUTOPILOT_SOCKET_FD _zsh_autopilot_receive
          return 0
        fi
        sleep 0.05
      done
    fi
    _zsh_autopilot_notice_after_grace daemon_unreachable \
      "autopilotd is not responding; check ${XDG_STATE_HOME:-$HOME/.local/state}/autopilot/daemon.log"
    return 1
  fi
  typeset -g ZSH_AUTOPILOT_SOCKET_FD=$REPLY

  zle -F $ZSH_AUTOPILOT_SOCKET_FD _zsh_autopilot_receive
}

_zsh_autopilot_socket_alive() {
  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && { true <&$ZSH_AUTOPILOT_SOCKET_FD } 2>/dev/null
}

_zsh_autopilot_write_line() {
  local line="$1"

  _zsh_autopilot_socket_alive || _zsh_autopilot_connect || return 1

  if ! print -r -u $ZSH_AUTOPILOT_SOCKET_FD -- "$line" 2>/dev/null; then
    _zsh_autopilot_connect || return 1
    print -r -u $ZSH_AUTOPILOT_SOCKET_FD -- "$line" 2>/dev/null || return 1
  fi
}

# $1 = buffer, $2 = kind (typing|next_command).
_zsh_autopilot_send() {
  local buffer="$1" kind="${2:-typing}"

  _zsh_autopilot_socket_alive || _zsh_autopilot_connect || return 1

  (( _ZSH_AUTOPILOT_SEQ++ ))
  typeset -g _ZSH_AUTOPILOT_REQ_ID="${ZSH_AUTOPILOT_SESSION_ID}.${_ZSH_AUTOPILOT_SEQ}"
  # METRICS(§12): capture the send-time anchor for total_latency_ms.
  whence -w _zsh_autopilot_metric_t0 &>/dev/null && _zsh_autopilot_metric_t0

  local REPLY
  _zsh_autopilot_json_escape "$buffer"
  # v:2; history is daemon-filled, not sent here.
  local line='{"v":2,"id":"'${_ZSH_AUTOPILOT_REQ_ID}'","kind":"'${kind}'","buf":"'${REPLY}'"'

  _zsh_autopilot_json_escape "$PWD"
  line+=',"cwd":"'${REPLY}'"'

  if [[ -n $_ZSH_AUTOPILOT_GIT_BRANCH ]]; then
    _zsh_autopilot_json_escape "$_ZSH_AUTOPILOT_GIT_BRANCH"
    line+=',"git_branch":"'${REPLY}'","git_dirty":'${_ZSH_AUTOPILOT_GIT_DIRTY}
  fi

  (( _ZSH_AUTOPILOT_LAST_EXIT != 0 )) && line+=',"last_exit":'${_ZSH_AUTOPILOT_LAST_EXIT}

  # Distinct loop var: reusing an already-local name here prints `item=...` to stdout.
  if (( ${#_ZSH_AUTOPILOT_DIR_ENTRIES} > 0 )); then
    local de_json='' entry
    for entry in "${_ZSH_AUTOPILOT_DIR_ENTRIES[@]}"; do
      _zsh_autopilot_json_escape "$entry"
      de_json+=${de_json:+,}'"'${REPLY}'"'
    done
    line+=',"dir_entries":['${de_json}']'
  fi

  line+='}'

  _zsh_autopilot_write_line "$line"
}

# $1 = cmd, $2 = cwd, $3 = ts. Must not set _ZSH_AUTOPILOT_REQ_ID: there is
# no reply to supersede.
_zsh_autopilot_send_record() {
  local cmd="$1" cwd="$2" ts="$3"

  (( _ZSH_AUTOPILOT_SEQ++ ))
  local id="${ZSH_AUTOPILOT_SESSION_ID}.${_ZSH_AUTOPILOT_SEQ}"

  local REPLY
  _zsh_autopilot_json_escape "$cwd"
  local line='{"v":2,"id":"'${id}'","kind":"record","cwd":"'${REPLY}'"'

  _zsh_autopilot_json_escape "$cmd"
  line+=',"cmd":"'${REPLY}'","ts":'${ts}'}'

  _zsh_autopilot_write_line "$line"
}

_zsh_autopilot_precmd() {
  _zsh_autopilot_send '' next_command
}

# zle -F callback. $1 = fd; $2 = error condition (hup/err/nval) or empty
# when data is readable.
_zsh_autopilot_receive() {
  emulate -L zsh
  local fd=$1

  if [[ -n "$2" ]]; then
    zle -F $fd
    exec {fd}<&- 2>/dev/null
    [[ $fd == $ZSH_AUTOPILOT_SOCKET_FD ]] && unset ZSH_AUTOPILOT_SOCKET_FD
    return
  fi

  # Reads one newline-framed JSON reply; the zle -F handler stays registered.
  local line
  IFS= read -r -u $fd line || return

  local REPLY

  # Parsed before the id check: a notice is session-level and must survive
  # a superseded reply.
  local notice notice_kind
  if _zsh_autopilot_json_str_field "$line" notice && [[ -n $REPLY ]]; then
    notice=$REPLY
    _zsh_autopilot_json_str_field "$line" notice_kind && notice_kind=$REPLY
    _zsh_autopilot_notice "${notice_kind:-unknown}" "$notice"
  fi

  _zsh_autopilot_json_str_field "$line" id || return
  [[ $REPLY == $_ZSH_AUTOPILOT_REQ_ID ]] || return
  local reply_source
  local suggestion
  _zsh_autopilot_json_str_field "$line" source && reply_source=$REPLY
  _zsh_autopilot_json_str_field "$line" suggestion && suggestion=$REPLY || return

  # METRICS(§12): paint anchor for the shown event; skipped when the reply
  # carries a notice, since nothing is painted then.
  if [[ -z $notice ]]; then
    whence -w _zsh_autopilot_metric_shown &>/dev/null && _zsh_autopilot_metric_shown "$_ZSH_AUTOPILOT_REQ_ID" "$suggestion"
  fi

  zle autopilot-suggest -- "$reply_source" "$suggestion"
}

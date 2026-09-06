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

zmodload zsh/datetime 2>/dev/null

# Seconds a shell tolerates an absent daemon before saying so, when autostart
# is off and something else owns the daemon's lifecycle. A shell launched
# alongside its daemon would otherwise pin a false alarm for the session.
typeset -gi _ZSH_AUTOPILOT_CONNECT_GRACE=10
typeset -gi _ZSH_AUTOPILOT_FIRST_CONNECT_FAIL=0

# Queues a notice only once this shell has been unable to reach a daemon for
# longer than the grace window. A cold start outlasts the retry loop in
# _zsh_autopilot_connect, and a notice pinned on that first miss is a false alarm.
_zsh_autopilot_notice_after_grace() {
  if (( ! _ZSH_AUTOPILOT_FIRST_CONNECT_FAIL )); then
    _ZSH_AUTOPILOT_FIRST_CONNECT_FAIL=$EPOCHSECONDS
    return
  fi
  (( EPOCHSECONDS - _ZSH_AUTOPILOT_FIRST_CONNECT_FAIL >= _ZSH_AUTOPILOT_CONNECT_GRACE )) &&
    _zsh_autopilot_notice "$1" "$2"
  return 0
}

# True when ZSH_AUTOPILOT_DAEMON_BIN names a runnable daemon. A value holding a
# slash is a path and is tested directly; $commands only ever holds bare names,
# so a path would always miss there.
_zsh_autopilot_daemon_bin_present() {
  [[ -n $ZSH_AUTOPILOT_DAEMON_BIN ]] || return 1
  if [[ $ZSH_AUTOPILOT_DAEMON_BIN == */* ]]; then
    [[ -x $ZSH_AUTOPILOT_DAEMON_BIN ]]
  else
    (( $+commands[$ZSH_AUTOPILOT_DAEMON_BIN] ))
  fi
}

# Fork the daemon in a subshell so it outlives this shell (no job-table entry
# to disown — the subshell itself exits right after backgrounding; nohup
# guards against SIGHUP on the off chance one is delivered first).
# Only tried once per shell session (_ZSH_AUTOPILOT_SPAWN_TRIED) — if the
# daemon is crash-looping, hammering fork on every connect attempt would make
# it worse, not better. The daemon's own single-instance guard (server.go)
# makes concurrent spawns from multiple shells race-safe: only one wins the
# socket bind, the rest exit immediately.
_zsh_autopilot_spawn_daemon() {
  (( _ZSH_AUTOPILOT_SPAWN_TRIED )) && return 1
  typeset -g _ZSH_AUTOPILOT_SPAWN_TRIED=1

  _zsh_autopilot_daemon_bin_present || return 1

  local log_dir="${XDG_STATE_HOME:-$HOME/.local/state}/autopilot"
  mkdir -p "$log_dir" 2>/dev/null

  # zsh does not export $HISTFILE, so it is passed explicitly. The assignment
  # prefix scopes it to this child alone; empty is fine, the daemon then falls
  # back to its own default.
  ( ZSH_AUTOPILOT_HISTFILE=$HISTFILE nohup "$ZSH_AUTOPILOT_DAEMON_BIN" -socket "$ZSH_AUTOPILOT_SOCKET" >>"$log_dir/daemon.log" 2>&1 & )
}

_zsh_autopilot_connect() {
  zmodload zsh/net/socket 2>/dev/null || return 1

  # Drop any stale fd before opening a new one.
  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && exec {ZSH_AUTOPILOT_SOCKET_FD}<&- 2>/dev/null

  if ! zsocket $ZSH_AUTOPILOT_SOCKET 2>/dev/null; then
    unset ZSH_AUTOPILOT_SOCKET_FD

    # Checked directly (not derived from spawn_daemon's return code, which is
    # also 1 for the once-per-shell guard and for a missing binary alike) so
    # the notice kind actually names what's wrong.
    local -i bin_present=0
    _zsh_autopilot_daemon_bin_present && bin_present=1

    if (( ! bin_present )); then
      # A named binary we can't run is a broken install: permanent, and worth
      # saying immediately.
      if [[ -n $ZSH_AUTOPILOT_DAEMON_BIN ]]; then
        _zsh_autopilot_notice no_daemon_bin \
          "daemon binary '$ZSH_AUTOPILOT_DAEMON_BIN' not found or not executable; reinstall zsh-autopilot or fix \$ZSH_AUTOPILOT_DAEMON_BIN"
        return 1
      fi
      # Empty means autostart was turned off deliberately, so someone else owns
      # the daemon's lifecycle and may still be starting it.
      _zsh_autopilot_notice_after_grace no_daemon \
        "nothing listening on $ZSH_AUTOPILOT_SOCKET and autostart is off (ZSH_AUTOPILOT_DAEMON_BIN is empty); start autopilotd yourself"
      return 1
    fi

    # Daemon not up (or not up yet) — spawn it once and give it a moment to
    # bind the socket, then retry. Short/bounded so a broken binary doesn't
    # stall shell startup.
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
    # Reached whether the spawn just fired or already fired earlier this shell,
    # so a daemon that never comes up is still reported once the grace window
    # closes. The caller degrades gracefully until then.
    _zsh_autopilot_notice_after_grace daemon_unreachable \
      "autopilotd is not responding; check ${XDG_STATE_HOME:-$HOME/.local/state}/autopilot/daemon.log"
    return 1
  fi
  typeset -g ZSH_AUTOPILOT_SOCKET_FD=$REPLY

  zle -F $ZSH_AUTOPILOT_SOCKET_FD _zsh_autopilot_receive
}

# true if $ZSH_AUTOPILOT_SOCKET_FD is a currently-open fd
_zsh_autopilot_socket_alive() {
  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && { true <&$ZSH_AUTOPILOT_SOCKET_FD } 2>/dev/null
}

# Write one already-serialized JSON line to the daemon: connect lazily if
# needed, and reconnect once on a write failure (half-open peer) before
# giving up.
_zsh_autopilot_write_line() {
  local line="$1"

  _zsh_autopilot_socket_alive || _zsh_autopilot_connect || return 1

  if ! print -r -u $ZSH_AUTOPILOT_SOCKET_FD -- "$line" 2>/dev/null; then
    _zsh_autopilot_connect || return 1
    print -r -u $ZSH_AUTOPILOT_SOCKET_FD -- "$line" 2>/dev/null || return 1
  fi
}

# Send a request to the daemon. $1 = buffer, $2 = kind (typing|next_command).
# Mints a fresh request id, records it as current, ships one JSON line with
# cwd/git_branch/git_dirty/last_exit when meaningful. Git state comes from
# 47_context.zsh's cache — forking git on the keystroke path would defeat it.
_zsh_autopilot_send() {
  local buffer="$1" kind="${2:-typing}"

  _zsh_autopilot_socket_alive || _zsh_autopilot_connect || return 1

  (( _ZSH_AUTOPILOT_SEQ++ ))
  typeset -g _ZSH_AUTOPILOT_REQ_ID="${ZSH_AUTOPILOT_SESSION_ID}.${_ZSH_AUTOPILOT_SEQ}"
  # METRICS(§12): capture the send-time anchor for total_latency_ms.
  whence -w _zsh_autopilot_metric_t0 &>/dev/null && _zsh_autopilot_metric_t0

  local REPLY
  _zsh_autopilot_json_escape "$buffer"
  # v:2 — `history` is daemon-filled (see protocol.Version), not sent here.
  local line='{"v":2,"id":"'${_ZSH_AUTOPILOT_REQ_ID}'","kind":"'${kind}'","buf":"'${REPLY}'"'

  _zsh_autopilot_json_escape "$PWD"
  line+=',"cwd":"'${REPLY}'"'

  if [[ -n $_ZSH_AUTOPILOT_GIT_BRANCH ]]; then
    _zsh_autopilot_json_escape "$_ZSH_AUTOPILOT_GIT_BRANCH"
    line+=',"git_branch":"'${REPLY}'","git_dirty":'${_ZSH_AUTOPILOT_GIT_DIRTY}
  fi

  (( _ZSH_AUTOPILOT_LAST_EXIT != 0 )) && line+=',"last_exit":'${_ZSH_AUTOPILOT_LAST_EXIT}

  # Distinct loop var (entry): re-declaring an already-`local` name across two
  # loops (e.g. both doing `local ... item`) makes zsh print `item=...` to
  # stdout — garbage on the prompt. Invisible to `zsh -n` and code review.
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

# Fire-and-forget record: $1 = cmd, $2 = cwd, $3 = ts. Draws an id from the
# shared _ZSH_AUTOPILOT_SEQ counter so the daemon can split out the session,
# but must not set _ZSH_AUTOPILOT_REQ_ID — there is no reply to supersede.
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

  local REPLY

  # Parsed before the id/suggestion gates below: a notice is about the
  # session, not one keystroke, and must survive a superseded reply.
  local notice notice_kind
  if _zsh_autopilot_json_str_field "$line" notice && [[ -n $REPLY ]]; then
    notice=$REPLY
    _zsh_autopilot_json_str_field "$line" notice_kind && notice_kind=$REPLY
    _zsh_autopilot_notice "${notice_kind:-unknown}" "$notice"
  fi

  # Correlate by id: ignore replies for a request we've already superseded.
  _zsh_autopilot_json_str_field "$line" id || return
  [[ $REPLY == $_ZSH_AUTOPILOT_REQ_ID ]] || return
  local reply_source
  local suggestion
  _zsh_autopilot_json_str_field "$line" source && reply_source=$REPLY
  _zsh_autopilot_json_str_field "$line" suggestion && suggestion=$REPLY || return

  # METRICS(§12): the reply matched our current request and is about to be
  # painted — this is the "shown" event's paint anchor. A notice-carrying
  # reply paints nothing, so counting it would inflate the shown denominator.
  if [[ -z $notice ]]; then
    whence -w _zsh_autopilot_metric_shown &>/dev/null && _zsh_autopilot_metric_shown "$_ZSH_AUTOPILOT_REQ_ID" "$suggestion"
  fi

  zle autopilot-suggest -- "$reply_source" "$suggestion"
}

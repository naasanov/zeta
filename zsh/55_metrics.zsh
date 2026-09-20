
#--------------------------------------------------------------------#
# Dev Metrics Event Log (§12, dogfooding only)                       #
#--------------------------------------------------------------------#

# TEMPORARY dogfooding default-ON (inverts the real default-OFF invariant);
# ZSH_AUTOPILOT_METRICS=0 disables it. Revert before the Phase-3 metrics strip.
# Every call site is a guarded `whence -w` check, so deleting this file no-ops them.

zmodload zsh/datetime 2>/dev/null

(( ! ${+ZSH_AUTOPILOT_METRICS_SOCKET} )) &&
typeset -g ZSH_AUTOPILOT_METRICS_SOCKET=/tmp/zsh-autopilot-metrics.sock

# Distinct fd var from the request socket's ZSH_AUTOPILOT_SOCKET_FD: a
# separate, write-only connection that must never be confused with it.
typeset -g _ZSH_AUTOPILOT_METRICS_SOCKET_FD=

# t0: send anchor, set at request-id mint in _zsh_autopilot_send.
typeset -gF _ZSH_AUTOPILOT_REQ_T0=0
# Paint anchor, set when a reply is about to be painted (time_to_accept_ms
# is measured from here).
typeset -gF _ZSH_AUTOPILOT_SHOWN_T=0
# request_id currently painted; "" = nothing showing. Cleared as part of
# emitting its outcome so at most one outcome is ever sent per shown id.
typeset -g _ZSH_AUTOPILOT_SHOWN_ID=
# Last accepted request_id, consumed by the preexec "executed" signal.
typeset -g _ZSH_AUTOPILOT_ACCEPTED_ID=

# true if metrics are turned on. TEMPORARY dogfooding default-ON: enabled
# unless explicitly disabled with ZSH_AUTOPILOT_METRICS=0 (or =false). Revert
# to `== 1` (default-OFF) before the Phase-3 metrics strip.
_zsh_autopilot_metrics_enabled() {
  [[ $ZSH_AUTOPILOT_METRICS != 0 && $ZSH_AUTOPILOT_METRICS != false ]]
}

_zsh_autopilot_metrics_connect() {
  zmodload zsh/net/socket 2>/dev/null || return 1

  [[ -n $_ZSH_AUTOPILOT_METRICS_SOCKET_FD ]] && exec {_ZSH_AUTOPILOT_METRICS_SOCKET_FD}<&- 2>/dev/null

  if ! zsocket $ZSH_AUTOPILOT_METRICS_SOCKET 2>/dev/null; then
    unset _ZSH_AUTOPILOT_METRICS_SOCKET_FD
    return 1 # metrics collector not up - degrade silently, never block typing
  fi
  typeset -g _ZSH_AUTOPILOT_METRICS_SOCKET_FD=$REPLY

  # Write-only: no `zle -F` registration, we never read from this socket.
}

# true if $_ZSH_AUTOPILOT_METRICS_SOCKET_FD is a currently-open fd
_zsh_autopilot_metrics_socket_alive() {
  [[ -n $_ZSH_AUTOPILOT_METRICS_SOCKET_FD ]] && { true <&$_ZSH_AUTOPILOT_METRICS_SOCKET_FD } 2>/dev/null
}

# Write one JSON line, fire-and-forget. Connects lazily on first use;
# reconnects once on a write failure (half-open peer), then gives up quietly.
_zsh_autopilot_metrics_send() {
  local line="$1"

  _zsh_autopilot_metrics_socket_alive || _zsh_autopilot_metrics_connect || return 1

  if ! print -r -u $_ZSH_AUTOPILOT_METRICS_SOCKET_FD -- "$line" 2>/dev/null; then
    _zsh_autopilot_metrics_connect || return 1
    print -r -u $_ZSH_AUTOPILOT_METRICS_SOCKET_FD -- "$line" 2>/dev/null || return 1
  fi
}

# JSON builder for the "outcome" event: exactly one row per painted
# suggestion. Whether the command actually ran isn't known until preexec,
# so that's a separate "executed" event, not a field here.
_zsh_autopilot_metrics_emit_outcome() {
  local request_id="$1" outcome="$2" accepted_chars="$3" time_to_accept_ms="$4"

  local ttam_fmt ts_fmt
  ttam_fmt=$(printf '%.1f' $time_to_accept_ms)
  ts_fmt=$(printf '%.3f' $EPOCHREALTIME)

  local REPLY
  _zsh_autopilot_json_escape "$request_id"
  local line='{"v":1,"event":"outcome","request_id":"'${REPLY}'","outcome":"'${outcome}'","accepted_chars":'${accepted_chars}',"time_to_accept_ms":'${ttam_fmt}',"ts":'${ts_fmt}'}'

  _zsh_autopilot_metrics_send "$line"
}

# One overwritten global is the correct anchor: zsh mints a new id per send
# and only ever paints the reply matching the *current* id.
_zsh_autopilot_metric_t0() {
  _zsh_autopilot_metrics_enabled || return 0
  typeset -g _ZSH_AUTOPILOT_REQ_T0=$EPOCHREALTIME
}

# $1 = request_id (already matched against $_ZSH_AUTOPILOT_REQ_ID by the
# caller), $2 = suggestion text.
_zsh_autopilot_metric_shown() {
  _zsh_autopilot_metrics_enabled || return 0
  (( _ZSH_AUTOPILOT_REQ_T0 == 0 )) && return 0

  local request_id="$1" suggestion="$2"
  local total_latency_ms=$(( (EPOCHREALTIME - _ZSH_AUTOPILOT_REQ_T0) * 1000 ))

  typeset -g _ZSH_AUTOPILOT_SHOWN_T=$EPOCHREALTIME
  typeset -g _ZSH_AUTOPILOT_SHOWN_ID=$request_id

  local latency_fmt ts_fmt
  latency_fmt=$(printf '%.1f' $total_latency_ms)
  ts_fmt=$(printf '%.3f' $EPOCHREALTIME)

  local REPLY
  _zsh_autopilot_json_escape "$request_id"
  # No buffer_len here: this fires outside ZLE widget context, so $BUFFER is
  # unset and ${#BUFFER} would silently read 0. It's already on the daemon's
  # "request" event, joinable via request_id.
  local line='{"v":1,"event":"shown","request_id":"'${REPLY}'","total_latency_ms":'${latency_fmt}',"suggestion_len":'${#suggestion}',"ts":'${ts_fmt}'}'

  _zsh_autopilot_metrics_send "$line"
}

# $1 = outcome (accepted|partial_accepted|typed_over|cleared), $2 =
# accepted_chars. Clears _ZSH_AUTOPILOT_SHOWN_ID immediately so a second
# widget invocation for the same keystroke is a no-op: one row per request.
_zsh_autopilot_metric_outcome() {
  _zsh_autopilot_metrics_enabled || return 0
  [[ -z $_ZSH_AUTOPILOT_SHOWN_ID ]] && return 0

  local outcome="$1" accepted_chars="${2:-0}"
  local request_id=$_ZSH_AUTOPILOT_SHOWN_ID
  typeset -g _ZSH_AUTOPILOT_SHOWN_ID=

  local time_to_accept_ms=$(( (EPOCHREALTIME - _ZSH_AUTOPILOT_SHOWN_T) * 1000 ))

  if [[ $outcome == accepted || $outcome == partial_accepted ]]; then
    typeset -g _ZSH_AUTOPILOT_ACCEPTED_ID=$request_id
  fi

  _zsh_autopilot_metrics_emit_outcome "$request_id" "$outcome" "$accepted_chars" "$time_to_accept_ms"
}

# A distinct event type, not a second "outcome" row: reusing "outcome" would
# double-count accepted suggestions in the acceptance-rate metric. Clears
# _ZSH_AUTOPILOT_ACCEPTED_ID so it fires once per accept.
_zsh_autopilot_metric_executed() {
  _zsh_autopilot_metrics_enabled || return 0
  [[ -z $_ZSH_AUTOPILOT_ACCEPTED_ID ]] && return 0

  local request_id=$_ZSH_AUTOPILOT_ACCEPTED_ID
  typeset -g _ZSH_AUTOPILOT_ACCEPTED_ID=

  local ts_fmt REPLY
  ts_fmt=$(printf '%.3f' $EPOCHREALTIME)
  _zsh_autopilot_json_escape "$request_id"

  _zsh_autopilot_metrics_send '{"v":1,"event":"executed","request_id":"'${REPLY}'","ts":'${ts_fmt}'}'
}

# A pointer row only (no buf/suggestion text; join on request_id). Falls
# back to REQ_ID when nothing is painted. Doesn't clear SHOWN_ID: flagging
# isn't an outcome and must not consume it.
_zsh_autopilot_metric_flag() {
  if ! _zsh_autopilot_metrics_enabled; then
    zle -M "autopilot: metrics disabled, nothing flagged (unset ZSH_AUTOPILOT_METRICS)"
    return 0
  fi

  local request_id=${_ZSH_AUTOPILOT_SHOWN_ID:-$_ZSH_AUTOPILOT_REQ_ID}
  if [[ -z $request_id ]]; then
    zle -M "autopilot: nothing to flag"
    return 0
  fi

  local ts_fmt REPLY
  ts_fmt=$(printf '%.3f' $EPOCHREALTIME)
  _zsh_autopilot_json_escape "$request_id"

  if _zsh_autopilot_metrics_send '{"v":1,"event":"flag","request_id":"'${REPLY}'","ts":'${ts_fmt}'}'; then
    zle -M "autopilot: flagged $request_id"
  else
    zle -M "autopilot: flag failed (metrics collector down?)"
  fi
}

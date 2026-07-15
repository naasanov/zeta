
#--------------------------------------------------------------------#
# Dev Metrics Event Log (§12, dogfooding only)                       #
#--------------------------------------------------------------------#
# Fire-and-forget instrumentation: one JSON line per "shown" (a suggestion got
# painted) and per "outcome" (what the user did with it), written to a
# write-only Unix socket the daemon side ingests. Off by default
# (ZSH_AUTOPILOT_METRICS unset) — every helper below gates on that first and
# returns immediately, so the disabled path is a cheap no-op.
#
# Removability: this fragment is meant to be deleted wholesale later. Every
# call site elsewhere in the plugin is a single guarded line of the form
#   whence -w _zsh_autopilot_metric_X &>/dev/null && _zsh_autopilot_metric_X ...
# so deleting this file turns each of those into a harmless no-op — no other
# fragment needs to change.
#
# Timing is computed entirely from zsh's own clock ($EPOCHREALTIME) at both
# ends; we never mix in a daemon-side timestamp (different process, clock
# skew would corrupt the advertised latency number).

zmodload zsh/datetime 2>/dev/null

(( ! ${+ZSH_AUTOPILOT_METRICS_SOCKET} )) &&
typeset -g ZSH_AUTOPILOT_METRICS_SOCKET=/tmp/zsh-autopilot-metrics.sock

# Distinct fd var from the request socket's ZSH_AUTOPILOT_SOCKET_FD — this is
# a separate, write-only connection and must never be confused with it.
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

# true if metrics are turned on.
_zsh_autopilot_metrics_enabled() {
  [[ $ZSH_AUTOPILOT_METRICS == 1 ]]
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

# JSON builder for the "outcome" event — exactly one row per painted
# suggestion (see _zsh_autopilot_metric_outcome). Whether the accepted command
# actually ran is NOT a field here: that isn't known until preexec, long after
# this row is sent. It's a separate "executed" event instead (see
# _zsh_autopilot_metric_executed).
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

# Called from _zsh_autopilot_send (50_socket.zsh) right at request-id mint.
# One overwritten global is the correct anchor: zsh mints a new id per send
# and only ever paints the reply matching the *current* id.
_zsh_autopilot_metric_t0() {
  _zsh_autopilot_metrics_enabled || return 0
  typeset -g _ZSH_AUTOPILOT_REQ_T0=$EPOCHREALTIME
}

# Called from _zsh_autopilot_receive (50_socket.zsh) once the id-match check
# has passed and the reply is about to be painted. $1 = request_id (the
# already-matched $_ZSH_AUTOPILOT_REQ_ID), $2 = suggestion text.
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
  # No buffer_len here: this fires from _zsh_autopilot_receive's `zle -F`
  # fd callback, which runs OUTSIDE ZLE widget context, so $BUFFER is unset
  # and ${#BUFFER} would silently read 0 on every event. buffer_len already
  # lives on the daemon's "request" event and is joinable via request_id -
  # don't re-add it here.
  local line='{"v":1,"event":"shown","request_id":"'${REPLY}'","total_latency_ms":'${latency_fmt}',"suggestion_len":'${#suggestion}',"ts":'${ts_fmt}'}'

  _zsh_autopilot_metrics_send "$line"
}

# Called from the accept/partial_accept/clear/modify widgets in
# 40_widgets.zsh. $1 = outcome (accepted|partial_accepted|typed_over|
# cleared), $2 = accepted_chars (default 0).
#
# Correctness: only fires when a suggestion is actually showing
# (_ZSH_AUTOPILOT_SHOWN_ID non-empty), and clears that id immediately as part
# of emitting — so a subsequent widget invocation for the same keystroke
# (e.g. accept-line falling through the ZSH_AUTOPILOT_CLEAR_WIDGETS path
# right after an accept) finds SHOWN_ID already empty and is a no-op. This
# guarantees at most one outcome row per request_id.
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

# Called from the preexec hook (47_context.zsh) right before a command runs.
# Emits its OWN event type rather than a second "outcome" row for the same
# request_id: acceptance rate is an advertised number, computed as
# count(outcome='accepted') / count(shown), and a follow-up "accepted" row
# would double-count every accepted-and-executed suggestion and inflate it.
# Keeping "executed" a distinct event preserves exactly one outcome row per
# request_id, and the executed rate is still recoverable by joining on
# request_id. The daemon ingests these as loose passthrough maps, so a new
# event type costs it nothing.
#
# Clears _ZSH_AUTOPILOT_ACCEPTED_ID so it only ever fires once per accept.
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

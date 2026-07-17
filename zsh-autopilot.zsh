# zsh-autopilot.zsh — GENERATED FILE, DO NOT EDIT.
# Built from zsh/*.zsh by `make plugin`; edit the fragments there.
#
# MIT License
# 
# Copyright (c) 2026 Nicolas Asanov
# 
# Portions adapted from zsh-autosuggestions (MIT), Copyright (c) Eric Freese.
# 
# Permission is hereby granted, free of charge, to any person obtaining a copy
# of this software and associated documentation files (the "Software"), to deal
# in the Software without restriction, including without limitation the rights
# to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
# copies of the Software, and to permit persons to whom the Software is
# furnished to do so, subject to the following conditions:
# 
# The above copyright notice and this permission notice shall be included in all
# copies or substantial portions of the Software.
# 
# THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
# IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
# FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
# AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
# LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
# OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
# SOFTWARE.


#--------------------------------------------------------------------#
# Global Configuration Variables                                     #
#--------------------------------------------------------------------#

# Color to use when highlighting suggestion
# Uses format of `region_highlight`
# More info: http://zsh.sourceforge.net/Doc/Release/Zsh-Line-Editor.html#Zle-Widgets
(( ! ${+ZSH_AUTOPILOT_HIGHLIGHT_STYLE} )) &&
typeset -g ZSH_AUTOPILOT_HIGHLIGHT_STYLE='fg=8'

# Prefix to use when saving original versions of bound widgets
(( ! ${+ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX} )) &&
typeset -g ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX=autopilot-orig-

# Path to the autopilotd (or Phase 0 echo-server) Unix socket. Client and
# daemon must agree; matches the echo-server default so no -socket flag is
# needed. Kept short deliberately: macOS caps socket paths at ~104 bytes.
(( ! ${+ZSH_AUTOPILOT_SOCKET} )) &&
typeset -g ZSH_AUTOPILOT_SOCKET=/tmp/zsh-autopilot.sock

# Daemon binary to lazy-spawn (must be on $PATH) when the socket isn't up —
# see 50_socket.zsh's _zsh_autopilot_spawn_daemon. Set to empty to disable
# autostart and rely on the daemon being launched some other way (a launchd/
# systemd unit, the VS Code debug launch, manual `autopilotd &`).
(( ! ${+ZSH_AUTOPILOT_DAEMON_BIN} )) &&
typeset -g ZSH_AUTOPILOT_DAEMON_BIN=autopilotd

# TEMPORARY (dogfooding): background self-update on shell startup — see
# zsh/66_update.zsh. AUTOUPDATE=0 disables it; INTERVAL throttles how often the
# check may run (seconds; 0 = every shell). URL is the install script it re-runs
# (which no-ops when already on the latest release). Remove this block and the
# 66_update.zsh fragment before release — real updates go via brew/plugin-manager.
(( ! ${+ZSH_AUTOPILOT_AUTOUPDATE} )) &&
typeset -g ZSH_AUTOPILOT_AUTOUPDATE=1
(( ! ${+ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL} )) &&
typeset -gi ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL=14400
(( ! ${+ZSH_AUTOPILOT_INSTALL_URL} )) &&
typeset -g ZSH_AUTOPILOT_INSTALL_URL=https://raw.githubusercontent.com/naasanov/zeta/main/scripts/install.sh

# Number of recent commands kept for the "history" context field sent with
# each request (oldest first). Small and bounded — this rides along on every
# keystroke burst, not just next-command requests, so keep it short.
(( ! ${+ZSH_AUTOPILOT_HISTORY_SIZE} )) &&
typeset -gi ZSH_AUTOPILOT_HISTORY_SIZE=10

# Widgets that clear the suggestion
(( ! ${+ZSH_AUTOPILOT_CLEAR_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_CLEAR_WIDGETS
  ZSH_AUTOPILOT_CLEAR_WIDGETS=(
    history-search-forward
    history-search-backward
    history-beginning-search-forward
    history-beginning-search-backward
    history-beginning-search-forward-end
    history-beginning-search-backward-end
    history-substring-search-up
    history-substring-search-down
    up-line-or-beginning-search
    down-line-or-beginning-search
    up-line-or-history
    down-line-or-history
    accept-line
    copy-earlier-word
  )
}

# Widgets that accept the entire suggestion
(( ! ${+ZSH_AUTOPILOT_ACCEPT_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_ACCEPT_WIDGETS
  ZSH_AUTOPILOT_ACCEPT_WIDGETS=(
    forward-char
    end-of-line
    vi-forward-char
    vi-end-of-line
    vi-add-eol
  )
}

# Widgets that accept the entire suggestion and execute it
(( ! ${+ZSH_AUTOPILOT_EXECUTE_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_EXECUTE_WIDGETS
  ZSH_AUTOPILOT_EXECUTE_WIDGETS=(
  )
}

# Widgets that accept the suggestion as far as the cursor moves
(( ! ${+ZSH_AUTOPILOT_PARTIAL_ACCEPT_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_PARTIAL_ACCEPT_WIDGETS
  ZSH_AUTOPILOT_PARTIAL_ACCEPT_WIDGETS=(
    forward-word
    emacs-forward-word
    vi-forward-word
    vi-forward-word-end
    vi-forward-blank-word
    vi-forward-blank-word-end
    vi-find-next-char
    vi-find-next-char-skip
  )
}

# Widgets that should be ignored (globbing supported but must be escaped)
(( ! ${+ZSH_AUTOPILOT_IGNORE_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_IGNORE_WIDGETS
  ZSH_AUTOPILOT_IGNORE_WIDGETS=(
    orig-\*
    beep
    run-help
    set-local-history
    which-command
    yank
    yank-pop
    zle-\*
  )
}

#--------------------------------------------------------------------#
# Widget Helpers                                                     #
#--------------------------------------------------------------------#

_zsh_autopilot_incr_bind_count() {
  typeset -gi bind_count=$((_ZSH_AUTOPILOT_BIND_COUNTS[$1]+1))
  _ZSH_AUTOPILOT_BIND_COUNTS[$1]=$bind_count
}

# Bind a single widget to an autopilot widget, saving a reference to the original widget
_zsh_autopilot_bind_widget() {
  typeset -gA _ZSH_AUTOPILOT_BIND_COUNTS

  local widget=$1
  local autopilot_action=$2
  local prefix=$ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX

  local -i bind_count

  # Save a reference to the original widget
  case $widgets[$widget] in
    # Already bound
    user:_zsh_autopilot_(bound|orig)_*)
      bind_count=$((_ZSH_AUTOPILOT_BIND_COUNTS[$widget]))
      ;;

    # User-defined widget
    user:*)
      _zsh_autopilot_incr_bind_count $widget
      zle -N $prefix$bind_count-$widget ${widgets[$widget]#*:}
      ;;

    # Built-in widget
    builtin)
      _zsh_autopilot_incr_bind_count $widget
      eval "_zsh_autopilot_orig_${(q)widget}() { zle .${(q)widget} }"
      zle -N $prefix$bind_count-$widget _zsh_autopilot_orig_$widget
      ;;

    # Completion widget
    completion:*)
      _zsh_autopilot_incr_bind_count $widget
      eval "zle -C $prefix$bind_count-${(q)widget} ${${(s.:.)widgets[$widget]}[2,3]}"
      ;;
  esac

  # Pass the original widget's name explicitly into the autopilot
  # function. Use this passed in widget name to call the original
  # widget instead of relying on the $WIDGET variable being set
  # correctly. $WIDGET cannot be trusted because other plugins call
  # zle without the `-w` flag (e.g. `zle self-insert` instead of
  # `zle self-insert -w`).
  eval "_zsh_autopilot_bound_${bind_count}_${(q)widget}() {
    _zsh_autopilot_widget_$autopilot_action $prefix$bind_count-${(q)widget} \$@
  }"

  # Create the bound widget
  zle -N -- $widget _zsh_autopilot_bound_${bind_count}_$widget
}

# Map all configured widgets to the right autopilot widgets
_zsh_autopilot_bind_widgets() {
  emulate -L zsh

  local widget
  local ignore_widgets

  ignore_widgets=(
    .\*
    _\*
    ${_ZSH_AUTOPILOT_BUILTIN_ACTIONS/#/autopilot-}
    $ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX\*
    $ZSH_AUTOPILOT_IGNORE_WIDGETS
  )

  # Find every widget we might want to bind and bind it appropriately
  for widget in ${${(f)"$(builtin zle -la)"}:#${(j:|:)~ignore_widgets}}; do
    if [[ -n ${ZSH_AUTOPILOT_CLEAR_WIDGETS[(r)$widget]} ]]; then
      _zsh_autopilot_bind_widget $widget clear
    elif [[ -n ${ZSH_AUTOPILOT_ACCEPT_WIDGETS[(r)$widget]} ]]; then
      _zsh_autopilot_bind_widget $widget accept
    elif [[ -n ${ZSH_AUTOPILOT_EXECUTE_WIDGETS[(r)$widget]} ]]; then
      _zsh_autopilot_bind_widget $widget execute
    elif [[ -n ${ZSH_AUTOPILOT_PARTIAL_ACCEPT_WIDGETS[(r)$widget]} ]]; then
      _zsh_autopilot_bind_widget $widget partial_accept
    else
      # Assume any unspecified widget might modify the buffer
      _zsh_autopilot_bind_widget $widget modify
    fi
  done
}

# Given the name of an original widget and args, invoke it, if it exists
_zsh_autopilot_invoke_original_widget() {
  # Do nothing unless called with at least one arg
  (( $# )) || return 0

  local original_widget_name="$1"

  shift

  if (( ${+widgets[$original_widget_name]} )); then
    zle $original_widget_name -- $@
  fi
}

#--------------------------------------------------------------------#
# Highlighting                                                       #
#--------------------------------------------------------------------#

# If there was a highlight, remove it
_zsh_autopilot_highlight_reset() {
  typeset -g _ZSH_AUTOPILOT_LAST_HIGHLIGHT

  if [[ -n "$_ZSH_AUTOPILOT_LAST_HIGHLIGHT" ]]; then
    region_highlight=("${(@)region_highlight:#$_ZSH_AUTOPILOT_LAST_HIGHLIGHT}")
    unset _ZSH_AUTOPILOT_LAST_HIGHLIGHT
  fi
}

# If there's a suggestion, highlight it
_zsh_autopilot_highlight_apply() {
  typeset -g _ZSH_AUTOPILOT_LAST_HIGHLIGHT

  if (( $#POSTDISPLAY )); then
    typeset -g _ZSH_AUTOPILOT_LAST_HIGHLIGHT="$#BUFFER $(($#BUFFER + $#POSTDISPLAY)) $ZSH_AUTOPILOT_HIGHLIGHT_STYLE"
    region_highlight+=("$_ZSH_AUTOPILOT_LAST_HIGHLIGHT")
  else
    unset _ZSH_AUTOPILOT_LAST_HIGHLIGHT
  fi
}

#--------------------------------------------------------------------#
# Autopilot Widget Implementations                                   #
#--------------------------------------------------------------------#

# Clear the suggestion
_zsh_autopilot_clear() {
  # Remove the suggestion
  POSTDISPLAY=

  # METRICS(§12): outcome cleared
  whence -w _zsh_autopilot_metric_outcome &>/dev/null && _zsh_autopilot_metric_outcome cleared

  _zsh_autopilot_invoke_original_widget $@
}

# Modify the buffer and get a new suggestion
_zsh_autopilot_modify() {
  local -i retval

  # Only available in zsh >= 5.4
  local -i KEYS_QUEUED_COUNT

  # Save the contents of the buffer/postdisplay
  local orig_buffer="$BUFFER"
  local orig_postdisplay="$POSTDISPLAY"

  # Clear suggestion while waiting for next one
  POSTDISPLAY=

  # Original widget may modify the buffer
  _zsh_autopilot_invoke_original_widget $@
  retval=$?

  emulate -L zsh

  # Don't fetch a new suggestion if there's more input to be read immediately
  if (( $PENDING > 0 || $KEYS_QUEUED_COUNT > 0 )); then
    POSTDISPLAY="$orig_postdisplay"
    return $retval
  fi

  # Optimize if manually typing in the suggestion or if buffer hasn't changed
  if [[ "$BUFFER" = "$orig_buffer"* && "$orig_postdisplay" = "${BUFFER:$#orig_buffer}"* ]]; then
    POSTDISPLAY="${orig_postdisplay:$(($#BUFFER - $#orig_buffer))}"
    return $retval
  fi

  # METRICS(§12): buffer diverged from the shown suggestion (a real edit, not
  # just typing into it) — the suggestion was dropped instead of accepted.
  whence -w _zsh_autopilot_metric_outcome &>/dev/null && _zsh_autopilot_metric_outcome typed_over

  # Bail out if suggestions are disabled (latent kill-switch: set
  # _ZSH_AUTOPILOT_DISABLED to suppress fetching)
  if (( ${+_ZSH_AUTOPILOT_DISABLED} )); then
    return $?
  fi

  # Get a new suggestion if the buffer is not empty after modification
  if (( $#BUFFER > 0 )); then
    if [[ -z "$ZSH_AUTOPILOT_BUFFER_MAX_SIZE" ]] || (( $#BUFFER <= $ZSH_AUTOPILOT_BUFFER_MAX_SIZE )); then
      _zsh_autopilot_fetch
    fi
  fi

  return $retval
}

# Fetch a new suggestion for the current buffer by asking the daemon.
#
# Unlike zsh-autosuggestions (which forks a subshell to run a local strategy),
# we hand the buffer to the socket transport, which ships it to autopilotd and
# paints the async reply via `zle autopilot-suggest`. `_zsh_autopilot_send`
# lives in the socket transport fragment (50_socket.zsh); until that is
# implemented this degrades to a no-op so the widget/ghost-text loop still runs.
_zsh_autopilot_fetch() {
  whence -w _zsh_autopilot_send &>/dev/null && _zsh_autopilot_send "$BUFFER" typing
  return 0
}

# Offer a suggestion. Invoked as `zle autopilot-suggest -- "$source" "$suggestion"`
# by the socket transport when the daemon's reply arrives. This is the seam
# where the daemon's string becomes ghost text. $source (llm|history) is carried
# through from the protocol's source tag; Phase 1 paints regardless of source,
# but the seam is here so the Phase 4a history/upgrade rendering rules slot in
# without touching the widget's caller.
_zsh_autopilot_suggest() {
  emulate -L zsh

  local source="$1"
  local suggestion="$2"

  # Paint whenever we have a suggestion — including on an empty buffer, which
  # is the next-command (precmd) case. With an empty BUFFER the prefix strip
  # is a no-op, so POSTDISPLAY becomes the whole suggested command.
  if [[ -n "$suggestion" ]]; then
    POSTDISPLAY="${suggestion#$BUFFER}"
  else
    POSTDISPLAY=
  fi
}

# Accept the entire suggestion
_zsh_autopilot_accept() {
  local -i retval max_cursor_pos=$#BUFFER

  # When vicmd keymap is active, the cursor can't move all the way
  # to the end of the buffer
  if [[ "$KEYMAP" = "vicmd" ]]; then
    max_cursor_pos=$((max_cursor_pos - 1))
  fi

  # If we're not in a valid state to accept a suggestion, just run the
  # original widget and bail out
  if (( $CURSOR != $max_cursor_pos || !$#POSTDISPLAY )); then
    _zsh_autopilot_invoke_original_widget $@
    return
  fi

  # Only accept if the cursor is at the end of the buffer
  # METRICS(§12): capture the accepted length before POSTDISPLAY is blanked.
  local _zsh_autopilot_metrics_accepted_chars=$#POSTDISPLAY

  # Add the suggestion to the buffer
  BUFFER="$BUFFER$POSTDISPLAY"

  # Remove the suggestion
  POSTDISPLAY=

  # METRICS(§12): outcome accepted
  whence -w _zsh_autopilot_metric_outcome &>/dev/null && _zsh_autopilot_metric_outcome accepted "$_zsh_autopilot_metrics_accepted_chars"

  # Run the original widget before manually moving the cursor so that the
  # cursor movement doesn't make the widget do something unexpected
  _zsh_autopilot_invoke_original_widget $@
  retval=$?

  # Move the cursor to the end of the buffer
  if [[ "$KEYMAP" = "vicmd" ]]; then
    CURSOR=$(($#BUFFER - 1))
  else
    CURSOR=$#BUFFER
  fi

  return $retval
}

# Accept the entire suggestion and execute it
_zsh_autopilot_execute() {
  # Add the suggestion to the buffer
  BUFFER="$BUFFER$POSTDISPLAY"

  # Remove the suggestion
  POSTDISPLAY=

  # Call the original `accept-line` to handle syntax highlighting or
  # other potential custom behavior
  _zsh_autopilot_invoke_original_widget "accept-line"
}

# Partially accept the suggestion
_zsh_autopilot_partial_accept() {
  local -i retval cursor_loc

  # Save the contents of the buffer so we can restore later if needed
  local original_buffer="$BUFFER"

  # Temporarily accept the suggestion.
  BUFFER="$BUFFER$POSTDISPLAY"

  # Original widget moves the cursor
  _zsh_autopilot_invoke_original_widget $@
  retval=$?

  # Normalize cursor location across vi/emacs modes
  cursor_loc=$CURSOR
  if [[ "$KEYMAP" = "vicmd" ]]; then
    cursor_loc=$((cursor_loc + 1))
  fi

  # If we've moved past the end of the original buffer
  if (( $cursor_loc > $#original_buffer )); then
    # Set POSTDISPLAY to text right of the cursor
    POSTDISPLAY="${BUFFER[$(($cursor_loc + 1)),$#BUFFER]}"

    # Clip the buffer at the cursor
    BUFFER="${BUFFER[1,$cursor_loc]}"

    # METRICS(§12): outcome partial_accepted, accepted_chars = chars actually taken
    whence -w _zsh_autopilot_metric_outcome &>/dev/null && _zsh_autopilot_metric_outcome partial_accepted "$(( cursor_loc - $#original_buffer ))"
  else
    # Restore the original buffer
    BUFFER="$original_buffer"
  fi

  return $retval
}

() {
  typeset -ga _ZSH_AUTOPILOT_BUILTIN_ACTIONS

  # Actions that get a registered `autopilot-<action>` ZLE widget. `suggest`
  # is here because the socket transport calls `zle autopilot-suggest`; the
  # rest are here so users can bind keys directly to them. `modify` and
  # `partial_accept` deliberately get widget *functions* (below) but no ZLE
  # widget — they are invoked through the bind trampoline, not by name.
  _ZSH_AUTOPILOT_BUILTIN_ACTIONS=(
    clear
    suggest
    accept
    execute
  )

  local action
  for action in $_ZSH_AUTOPILOT_BUILTIN_ACTIONS modify partial_accept; do
    eval "_zsh_autopilot_widget_$action() {
      local -i retval

      _zsh_autopilot_highlight_reset

      _zsh_autopilot_$action \$@
      retval=\$?

      _zsh_autopilot_highlight_apply

      zle -R

      return \$retval
    }"
  done

  for action in $_ZSH_AUTOPILOT_BUILTIN_ACTIONS; do
    zle -N autopilot-$action _zsh_autopilot_widget_$action
  done
}

#--------------------------------------------------------------------#
# Minimal JSON helpers                                               #
#--------------------------------------------------------------------#
# The wire protocol (daemon/internal/protocol) is newline-delimited JSON. zsh
# has no JSON tooling, and forking jq on every keystroke is exactly the
# fork-per-request cost the daemon exists to avoid (design §48). These two
# pure-zsh helpers cover what the client needs: escape a string into a JSON
# request, and pull a flat string field out of a one-line JSON reply.
#
# Scope/limits (documented, adequate for shell command lines):
#  - Encoding escapes " \ and the \n \t \r control chars. Other control bytes
#    (< 0x20) are passed through; command buffers don't contain them.
#  - Decoding understands the \" \\ \/ \n \t \r escapes. It does NOT decode
#    \uXXXX — which is why the daemon MUST encode with HTML escaping disabled so
#    shell metacharacters (< > &) stay literal (see the protocol package doc).

# Escape $1 as a JSON string body (no surrounding quotes) into $REPLY.
# Backslash is replaced first so the escapes we introduce aren't re-escaped.
_zsh_autopilot_json_escape() {
  emulate -L zsh
  local s=$1
  s=${s//'\'/'\\'}
  s=${s//'"'/'\"'}
  s=${s//$'\n'/'\n'}
  s=${s//$'\t'/'\t'}
  s=${s//$'\r'/'\r'}
  REPLY=$s
}

# Extract the string value of flat key $2 from one-line JSON object $1 into
# $REPLY. Returns non-zero if the key is absent. The regex tolerates escaped
# quotes inside the value ((\\.|[^"\\])*), then the escapes are undone. A
# sentinel byte (0x01) protects literal "\\" so a following n/t/r isn't misread
# as a control escape.
_zsh_autopilot_json_str_field() {
  emulate -L zsh
  local json=$1 key=$2
  local -a match mbegin mend
  local re
  re='"'${key}'":"((\\.|[^"\\])*)"'

  [[ $json =~ $re ]] || { REPLY=; return 1 }

  local raw=$match[1]
  raw=${raw//'\\'/$'\x01'}
  raw=${raw//'\n'/$'\n'}
  raw=${raw//'\t'/$'\t'}
  raw=${raw//'\r'/$'\r'}
  raw=${raw//'\"'/'"'}
  raw=${raw//'\/'/'/'}
  raw=${raw//$'\x01'/'\'}
  REPLY=$raw
  return 0
}

#--------------------------------------------------------------------#
# Context Capture (cwd / git / last exit / recent history)           #
#--------------------------------------------------------------------#
# Cheap, hook-driven context the socket transport (50_socket.zsh) rides along
# on every request. Nothing here runs per-keystroke: git state is computed on
# precmd/chpwd only and cached in globals; `_zsh_autopilot_send` just reads
# the cache. Running `git` on every keystroke would reintroduce the
# fork/exec-per-request cost this whole daemon architecture exists to avoid
# (design §7).

# Cached context globals. Empty/zero/false are the "nothing to report"
# values that the socket transport uses to omit a field entirely.
typeset -gi _ZSH_AUTOPILOT_LAST_EXIT=0
typeset -g _ZSH_AUTOPILOT_GIT_BRANCH=
typeset -g _ZSH_AUTOPILOT_GIT_DIRTY=false
typeset -ga _ZSH_AUTOPILOT_HISTORY

# precmd hook: capture the previous command's exit status. This MUST be the
# very first statement of this function (and this function should be
# registered as early as possible in the precmd chain) so nothing — not even
# a harmless-looking builtin — clobbers $? before we read it.
_zsh_autopilot_capture_exit() {
  typeset -gi _ZSH_AUTOPILOT_LAST_EXIT=$?
}

# precmd + chpwd hook: refresh the cached git branch/dirty state. Two cheap
# git invocations, but only ever on a fresh prompt or directory change — never
# in the per-keystroke send path.
_zsh_autopilot_refresh_git() {
  emulate -L zsh

  typeset -g _ZSH_AUTOPILOT_GIT_BRANCH=
  typeset -g _ZSH_AUTOPILOT_GIT_DIRTY=false

  local branch
  # No branch (not a repo, or detached with no symbolic ref) -> leave the
  # cache cleared; the socket transport treats an empty branch as "not a repo"
  # and omits both git_branch and git_dirty.
  branch=$(git symbolic-ref --quiet --short HEAD 2>/dev/null) || return
  _ZSH_AUTOPILOT_GIT_BRANCH=$branch

  [[ -n $(git status --porcelain --untracked-files=no 2>/dev/null) ]] &&
    _ZSH_AUTOPILOT_GIT_DIRTY=true
}

# preexec hook: append the about-to-run command to the bounded recent-history
# list. $1 is the raw command line as typed (preexec's first arg), oldest
# entries fall off the front so the array stays oldest-first, newest-last.
_zsh_autopilot_track_history() {
  emulate -L zsh

  local cmd="$1"
  [[ -z $cmd ]] && return

  # METRICS(§12): signal that a previously accepted suggestion actually ran.
  whence -w _zsh_autopilot_metric_executed &>/dev/null && _zsh_autopilot_metric_executed

  _ZSH_AUTOPILOT_HISTORY+=("$cmd")

  local -i max=${ZSH_AUTOPILOT_HISTORY_SIZE:-10}
  (( max < 1 )) && max=1
  if (( ${#_ZSH_AUTOPILOT_HISTORY} > max )); then
    _ZSH_AUTOPILOT_HISTORY=("${(@)_ZSH_AUTOPILOT_HISTORY[-max,-1]}")
  fi
}

autoload -Uz add-zsh-hook

# Registered here (fragment 55, before 60_start.zsh's precmd hooks) so that
# by the time _zsh_autopilot_precmd (the next-command request) fires, $?/git
# are already fresh. add-zsh-hook runs hooks in registration order, and
# source order across the numbered fragments is what fixes that order.
add-zsh-hook precmd _zsh_autopilot_capture_exit
add-zsh-hook precmd _zsh_autopilot_refresh_git
add-zsh-hook chpwd _zsh_autopilot_refresh_git
add-zsh-hook preexec _zsh_autopilot_track_history

# Seed the git cache immediately so context is sane even before the first
# precmd runs (e.g. a suggestion request triggered while typing on the very
# first prompt).
_zsh_autopilot_refresh_git
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

  [[ -n $ZSH_AUTOPILOT_DAEMON_BIN ]] || return 1
  (( $+commands[$ZSH_AUTOPILOT_DAEMON_BIN] )) || return 1

  local log_dir="${XDG_STATE_HOME:-$HOME/.local/state}/autopilot"
  mkdir -p "$log_dir" 2>/dev/null

  ( nohup "$ZSH_AUTOPILOT_DAEMON_BIN" -socket "$ZSH_AUTOPILOT_SOCKET" >>"$log_dir/daemon.log" 2>&1 & )
}

_zsh_autopilot_connect() {
  zmodload zsh/net/socket 2>/dev/null || return 1

  # Drop any stale fd before opening a new one.
  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && exec {ZSH_AUTOPILOT_SOCKET_FD}<&- 2>/dev/null

  if ! zsocket $ZSH_AUTOPILOT_SOCKET 2>/dev/null; then
    unset ZSH_AUTOPILOT_SOCKET_FD

    # Daemon not up (or not up yet) — spawn it once and give it a moment to
    # bind the socket, then retry. Short/bounded so a broken binary doesn't
    # stall shell startup.
    if _zsh_autopilot_spawn_daemon; then
      local -i tries=0 connected=0
      while (( tries++ < 10 )); do
        zsocket $ZSH_AUTOPILOT_SOCKET 2>/dev/null && { connected=1; break }
        sleep 0.05
      done
      if (( connected )); then
        typeset -g ZSH_AUTOPILOT_SOCKET_FD=$REPLY
        zle -F $ZSH_AUTOPILOT_SOCKET_FD _zsh_autopilot_receive
        return 0
      fi
    fi
    return 1 # still not up - caller degrades gracefully
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
#
# Optional context fields ride along on every request (cwd/git_branch/
# git_dirty/last_exit/history), each included only when it has a meaningful
# value to report. Git state and history are read from the caches maintained
# by 47_context.zsh's precmd/chpwd/preexec hooks — nothing here forks git or
# walks history; that would defeat the point of caching on the hot per-
# keystroke path.
_zsh_autopilot_send() {
  local buffer="$1" kind="${2:-typing}"

  _zsh_autopilot_socket_alive || _zsh_autopilot_connect || return 1

  (( _ZSH_AUTOPILOT_SEQ++ ))
  typeset -g _ZSH_AUTOPILOT_REQ_ID="${ZSH_AUTOPILOT_SESSION_ID}.${_ZSH_AUTOPILOT_SEQ}"
  # METRICS(§12): capture the send-time anchor for total_latency_ms.
  whence -w _zsh_autopilot_metric_t0 &>/dev/null && _zsh_autopilot_metric_t0

  local REPLY
  _zsh_autopilot_json_escape "$buffer"
  local line='{"v":1,"id":"'${_ZSH_AUTOPILOT_REQ_ID}'","kind":"'${kind}'","buf":"'${REPLY}'"'

  _zsh_autopilot_json_escape "$PWD"
  line+=',"cwd":"'${REPLY}'"'

  if [[ -n $_ZSH_AUTOPILOT_GIT_BRANCH ]]; then
    _zsh_autopilot_json_escape "$_ZSH_AUTOPILOT_GIT_BRANCH"
    line+=',"git_branch":"'${REPLY}'","git_dirty":'${_ZSH_AUTOPILOT_GIT_DIRTY}
  fi

  (( _ZSH_AUTOPILOT_LAST_EXIT != 0 )) && line+=',"last_exit":'${_ZSH_AUTOPILOT_LAST_EXIT}

  if (( ${#_ZSH_AUTOPILOT_HISTORY} > 0 )); then
    local hist_json='' item
    for item in "${_ZSH_AUTOPILOT_HISTORY[@]}"; do
      _zsh_autopilot_json_escape "$item"
      hist_json+=${hist_json:+,}'"'${REPLY}'"'
    done
    line+=',"history":['${hist_json}']'
  fi

  line+='}'

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

  # METRICS(§12): the reply matched our current request and is about to be
  # painted — this is the "shown" event's paint anchor.
  whence -w _zsh_autopilot_metric_shown &>/dev/null && _zsh_autopilot_metric_shown "$_ZSH_AUTOPILOT_REQ_ID" "$suggestion"

  zle autopilot-suggest -- "$reply_source" "$suggestion"
}

#--------------------------------------------------------------------#
# Dev Metrics Event Log (§12, dogfooding only)                       #
#--------------------------------------------------------------------#
# Fire-and-forget instrumentation: one JSON line per "shown" (a suggestion got
# painted) and per "outcome" (what the user did with it), written to a
# write-only Unix socket the daemon side ingests. TEMPORARY dogfooding
# default-ON (inverts the §12 default-OFF invariant so friends' installs emit
# without editing .zshrc; revert before the Phase-3 metrics strip) — set
# ZSH_AUTOPILOT_METRICS=0 to disable. Every helper below gates on
# _zsh_autopilot_metrics_enabled first and returns immediately, so the disabled
# path is a cheap no-op.
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

#--------------------------------------------------------------------#
# Start                                                              #
#--------------------------------------------------------------------#

# Start the autopilot widgets
_zsh_autopilot_start() {
  # By default we re-bind widgets on every precmd to ensure we wrap other
  # wrappers. Specifically, highlighting breaks if our widgets are wrapped by
  # zsh-syntax-highlighting widgets. This also allows modifications to the
  # widget list variables to take effect on the next precmd. However this has
  # a decent performance hit, so users can set ZSH_AUTOPILOT_MANUAL_REBIND
  # to disable the automatic re-binding.
  if (( ${+ZSH_AUTOPILOT_MANUAL_REBIND} )); then
    add-zsh-hook -d precmd _zsh_autopilot_start
  fi

  _zsh_autopilot_bind_widgets
}

# Mark the functions that we use for autoloading
autoload -Uz add-zsh-hook

# Start the autopilot widgets on the next precmd
add-zsh-hook precmd _zsh_autopilot_start

# Request a next-command suggestion on each fresh prompt (Phase 0 goal d).
add-zsh-hook precmd _zsh_autopilot_precmd

# Open the warm socket now so the first prompt already has a connection.
_zsh_autopilot_connect

#--------------------------------------------------------------------#
# Background self-update (TEMPORARY — dogfooding only)                #
#--------------------------------------------------------------------#
# While dogfooding we want friends to pick up new releases without re-running
# the installer by hand. At shell startup — at most once per
# ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL seconds — fork a fully-detached job that
# re-runs the published install script. That script is version-aware: it exits
# immediately when already on the latest release, and on a real update it swaps
# the binary/bundle and stops the running daemon so the NEXT new terminal
# lazy-spawns the new one (this shell keeps the old daemon until then — the
# spawn-once guard in 50_socket.zsh won't respawn mid-session).
#
# Non-blocking by construction: the foreground shell never waits on the network
# (the whole check is backgrounded). The throttle keeps many terminals from
# hammering GitHub's unauthenticated API rate limit.
#
# Remove this fragment and its config block in 10_config.zsh before release —
# real distribution updates go through brew / plugin managers, not curl|sh on
# startup.

zmodload zsh/datetime 2>/dev/null

_zsh_autopilot_autoupdate() {
  emulate -L zsh

  [[ $ZSH_AUTOPILOT_AUTOUPDATE == 0 ]] && return
  (( $+commands[curl] )) || return
  [[ -n $ZSH_AUTOPILOT_INSTALL_URL ]] || return

  local dir="${XDG_DATA_HOME:-$HOME/.local/share}/zsh-autopilot"
  local stamp="$dir/.last-update-check"
  local -i now=${EPOCHSECONDS:-0} interval=${ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL:-14400}
  local -i last=0
  [[ -r $stamp ]] && last=$(<$stamp) 2>/dev/null

  # Throttle: skip if we checked within the interval. Stamp BEFORE forking so a
  # burst of new terminals in the same window fire at most one check.
  (( now - last < interval )) && return
  mkdir -p "$dir" 2>/dev/null
  print -r -- $now >| "$stamp" 2>/dev/null

  # Fully detached; all output to the update log so nothing lands on the prompt.
  ( nohup sh -c "curl -fsSL '$ZSH_AUTOPILOT_INSTALL_URL' | sh" \
      >>"$dir/update.log" 2>&1 & )
}

_zsh_autopilot_autoupdate

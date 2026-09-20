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

# region_highlight-style color spec, e.g. 'fg=8'.
(( ! ${+ZSH_AUTOPILOT_HIGHLIGHT_STYLE} )) &&
typeset -g ZSH_AUTOPILOT_HIGHLIGHT_STYLE='fg=8'

(( ! ${+ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX} )) &&
typeset -g ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX=autopilot-orig-

# macOS caps Unix socket paths at ~104 bytes; keep this short.
(( ! ${+ZSH_AUTOPILOT_SOCKET} )) &&
typeset -g ZSH_AUTOPILOT_SOCKET=/tmp/zsh-autopilot.sock

# Daemon binary to lazy-spawn when the socket isn't up.
# Empty disables autostart; something else must launch the daemon then.
(( ! ${+ZSH_AUTOPILOT_DAEMON_BIN} )) &&
typeset -g ZSH_AUTOPILOT_DAEMON_BIN=autopilotd

# TEMPORARY (dogfooding): background self-update on shell startup.
# AUTOUPDATE=0 disables it; INTERVAL throttles the check (seconds, 0 = every shell).
# Remove this block and 66_update.zsh before release.
(( ! ${+ZSH_AUTOPILOT_AUTOUPDATE} )) &&
typeset -g ZSH_AUTOPILOT_AUTOUPDATE=1
(( ! ${+ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL} )) &&
typeset -gi ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL=14400
(( ! ${+ZSH_AUTOPILOT_INSTALL_URL} )) &&
typeset -g ZSH_AUTOPILOT_INSTALL_URL=https://raw.githubusercontent.com/naasanov/zeta/main/scripts/install.sh

# Key sequence bound to autopilot-flag (METRICS §12). Empty disables the
# default binding. ^Xf is unbound in the stock zsh emacs keymap (unlike
# Alt-f/^[f, which is forward-word).
(( ! ${+ZSH_AUTOPILOT_FLAG_KEY} )) &&
typeset -g ZSH_AUTOPILOT_FLAG_KEY='^Xf'

# Whether this shell reports commands to the daemon's history store.
# 0 keeps this shell's runs out of history entirely (suggestions still work).
(( ! ${+ZSH_AUTOPILOT_RECORD} )) &&
typeset -gi ZSH_AUTOPILOT_RECORD=1

# Whether the client prints diagnostic notices (auth failures, missing
# daemon binary, updates) to stderr above the next prompt. 0 disables.
(( ! ${+ZSH_AUTOPILOT_NOTICES} )) &&
typeset -g ZSH_AUTOPILOT_NOTICES=1

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

(( ! ${+ZSH_AUTOPILOT_EXECUTE_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_EXECUTE_WIDGETS
  ZSH_AUTOPILOT_EXECUTE_WIDGETS=(
  )
}

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

# Entries may be globs; a literal `*` must be escaped (e.g. `orig-\*`).
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

  # $WIDGET can't be trusted: other plugins call zle without -w (e.g. plain
  # `zle self-insert`), so the original widget name is passed explicitly
  # instead.
  eval "_zsh_autopilot_bound_${bind_count}_${(q)widget}() {
    _zsh_autopilot_widget_$autopilot_action $prefix$bind_count-${(q)widget} \$@
  }"

  zle -N -- $widget _zsh_autopilot_bound_${bind_count}_$widget
}

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

_zsh_autopilot_invoke_original_widget() {
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

_zsh_autopilot_highlight_reset() {
  typeset -g _ZSH_AUTOPILOT_LAST_HIGHLIGHT

  if [[ -n "$_ZSH_AUTOPILOT_LAST_HIGHLIGHT" ]]; then
    region_highlight=("${(@)region_highlight:#$_ZSH_AUTOPILOT_LAST_HIGHLIGHT}")
    unset _ZSH_AUTOPILOT_LAST_HIGHLIGHT
  fi
}

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

_zsh_autopilot_clear() {
  POSTDISPLAY=

  # METRICS(§12): outcome cleared
  whence -w _zsh_autopilot_metric_outcome &>/dev/null && _zsh_autopilot_metric_outcome cleared

  _zsh_autopilot_invoke_original_widget $@
}

_zsh_autopilot_modify() {
  local -i retval

  # Only available in zsh >= 5.4
  local -i KEYS_QUEUED_COUNT

  local orig_buffer="$BUFFER"
  local orig_postdisplay="$POSTDISPLAY"

  POSTDISPLAY=

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
  # just typing into it), so the suggestion was dropped instead of accepted.
  whence -w _zsh_autopilot_metric_outcome &>/dev/null && _zsh_autopilot_metric_outcome typed_over

  # Bail out if suggestions are disabled (latent kill-switch: set
  # _ZSH_AUTOPILOT_DISABLED to suppress fetching)
  if (( ${+_ZSH_AUTOPILOT_DISABLED} )); then
    return $?
  fi

  if (( $#BUFFER > 0 )); then
    if [[ -z "$ZSH_AUTOPILOT_BUFFER_MAX_SIZE" ]] || (( $#BUFFER <= $ZSH_AUTOPILOT_BUFFER_MAX_SIZE )); then
      _zsh_autopilot_fetch
    fi
  fi

  return $retval
}

# Falls back to a no-op if the socket transport isn't loaded.
_zsh_autopilot_fetch() {
  whence -w _zsh_autopilot_send &>/dev/null && _zsh_autopilot_send "$BUFFER" typing
  return 0
}

_zsh_autopilot_suggest() {
  emulate -L zsh

  local source="$1"
  local suggestion="$2"

  # Paints on an empty buffer too (the next-command case): the prefix strip
  # is then a no-op, so POSTDISPLAY becomes the whole suggested command.
  if [[ -n "$suggestion" ]]; then
    POSTDISPLAY="${suggestion#$BUFFER}"
  else
    POSTDISPLAY=
  fi
}

_zsh_autopilot_accept() {
  local -i retval max_cursor_pos=$#BUFFER

  # vicmd keymap can't move the cursor all the way to the end of the buffer.
  if [[ "$KEYMAP" = "vicmd" ]]; then
    max_cursor_pos=$((max_cursor_pos - 1))
  fi

  # Bail to the original widget unless the cursor is at the end with a
  # suggestion showing.
  if (( $CURSOR != $max_cursor_pos || !$#POSTDISPLAY )); then
    _zsh_autopilot_invoke_original_widget $@
    return
  fi

  # METRICS(§12): capture the accepted length before POSTDISPLAY is blanked.
  local _zsh_autopilot_metrics_accepted_chars=$#POSTDISPLAY

  BUFFER="$BUFFER$POSTDISPLAY"
  POSTDISPLAY=

  # METRICS(§12): outcome accepted
  whence -w _zsh_autopilot_metric_outcome &>/dev/null && _zsh_autopilot_metric_outcome accepted "$_zsh_autopilot_metrics_accepted_chars"

  # Runs before the cursor move below so the move doesn't affect the widget.
  _zsh_autopilot_invoke_original_widget $@
  retval=$?

  if [[ "$KEYMAP" = "vicmd" ]]; then
    CURSOR=$(($#BUFFER - 1))
  else
    CURSOR=$#BUFFER
  fi

  return $retval
}

_zsh_autopilot_execute() {
  BUFFER="$BUFFER$POSTDISPLAY"
  POSTDISPLAY=

  # Invokes accept-line explicitly, not the passed-in widget, for its
  # highlighting and other side effects.
  _zsh_autopilot_invoke_original_widget "accept-line"
}

_zsh_autopilot_partial_accept() {
  local -i retval cursor_loc

  local original_buffer="$BUFFER"

  # Temporarily accepts the suggestion so the original widget's cursor math
  # runs against the full buffer; restored below if the cursor didn't move
  # into it.
  BUFFER="$BUFFER$POSTDISPLAY"

  _zsh_autopilot_invoke_original_widget $@
  retval=$?

  # Normalize cursor location across vi/emacs modes
  cursor_loc=$CURSOR
  if [[ "$KEYMAP" = "vicmd" ]]; then
    cursor_loc=$((cursor_loc + 1))
  fi

  if (( $cursor_loc > $#original_buffer )); then
    POSTDISPLAY="${BUFFER[$(($cursor_loc + 1)),$#BUFFER]}"
    BUFFER="${BUFFER[1,$cursor_loc]}"

    # METRICS(§12): outcome partial_accepted, accepted_chars = chars actually taken
    whence -w _zsh_autopilot_metric_outcome &>/dev/null && _zsh_autopilot_metric_outcome partial_accepted "$(( cursor_loc - $#original_buffer ))"
  else
    BUFFER="$original_buffer"
  fi

  return $retval
}

# METRICS(§12): flags the on-screen suggestion as a bad-output candidate.
# Must not touch BUFFER/POSTDISPLAY or emit an outcome; flagging isn't a
# thing the user did with the suggestion, so it must stay on screen.
_zsh_autopilot_flag() {
  whence -w _zsh_autopilot_metric_flag &>/dev/null && _zsh_autopilot_metric_flag
  return 0
}

() {
  typeset -ga _ZSH_AUTOPILOT_BUILTIN_ACTIONS

  # `suggest` needs a ZLE widget since the socket transport calls it by name;
  # the rest let users bind keys directly. Omitting an action here makes
  # _zsh_autopilot_bind_widgets treat it as `modify`.
  _ZSH_AUTOPILOT_BUILTIN_ACTIONS=(
    clear
    suggest
    accept
    execute
    flag # METRICS(§12)
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

  # METRICS(§12): default keybinding, opt out via ZSH_AUTOPILOT_FLAG_KEY=''.
  [[ -n $ZSH_AUTOPILOT_FLAG_KEY ]] && bindkey $ZSH_AUTOPILOT_FLAG_KEY autopilot-flag
}

#--------------------------------------------------------------------#
# Minimal JSON helpers                                               #
#--------------------------------------------------------------------#

# Pure-zsh JSON: forking jq on every keystroke defeats the daemon's purpose.
# Decoding does not handle \uXXXX, so the daemon must encode with HTML
# escaping off so shell metacharacters (< > &) stay literal.

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

# Extracts the string value of flat key $2 from one-line JSON $1 into $REPLY;
# returns non-zero if absent. A sentinel byte (0x01) protects literal "\\" so
# a following n/t/r isn't misread as a control escape.
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

# Git/dir state is cached from precmd/chpwd hooks, never computed per
# keystroke; running git on every keystroke would reintroduce the
# fork-per-request cost the daemon exists to avoid.

zmodload zsh/datetime 2>/dev/null

# Cached context globals. Empty/zero/false are the "nothing to report"
# values that the socket transport uses to omit a field entirely.
typeset -gi _ZSH_AUTOPILOT_LAST_EXIT=0
typeset -g _ZSH_AUTOPILOT_GIT_BRANCH=
typeset -g _ZSH_AUTOPILOT_GIT_DIRTY=false
typeset -ga _ZSH_AUTOPILOT_DIR_ENTRIES

# precmd hook: captures the previous command's exit status. Must be the very
# first statement here, and this hook must run first in the precmd chain, so
# nothing clobbers $? before it's read.
_zsh_autopilot_capture_exit() {
  typeset -gi _ZSH_AUTOPILOT_LAST_EXIT=$?
}

# precmd + chpwd hook: refreshes the cached git branch/dirty state. Two cheap
# git invocations, but only on a fresh prompt or directory change, never in
# the per-keystroke send path.
_zsh_autopilot_refresh_git() {
  emulate -L zsh

  typeset -g _ZSH_AUTOPILOT_GIT_BRANCH=
  typeset -g _ZSH_AUTOPILOT_GIT_DIRTY=false

  local branch
  # No branch (not a repo, or detached HEAD) leaves the cache cleared; an
  # empty git_branch means "not a repo" downstream.
  branch=$(git symbolic-ref --quiet --short HEAD 2>/dev/null) || return
  _ZSH_AUTOPILOT_GIT_BRANCH=$branch

  [[ -n $(git status --porcelain --untracked-files=no 2>/dev/null) ]] &&
    _ZSH_AUTOPILOT_GIT_DIRTY=true
}

# precmd + chpwd hook: refreshes the cached directory listing. precmd (not
# just chpwd) is required so files created in the current dir (e.g. after
# `touch foo`) still show up.
_zsh_autopilot_refresh_dir() {
  emulate -L zsh

  typeset -g _ZSH_AUTOPILOT_DIR_ENTRIES=()

  local -a e
  e=( *(N) )

  (( ${#e} >= 1 && ${#e} <= 50 )) && _ZSH_AUTOPILOT_DIR_ENTRIES=("${e[@]}")
}

# preexec hook: reports the about-to-run command to the daemon's history
# store. $1 is unexpanded (needed for hist_ignore_space); $PWD is the dir the
# command runs IN, so `cd ..` is tagged with the pre-cd directory.
_zsh_autopilot_record() {
  emulate -L zsh

  local cmd="$1"
  [[ -z $cmd ]] && return

  # METRICS(§12): signal that a previously accepted suggestion actually ran.
  whence -w _zsh_autopilot_metric_executed &>/dev/null && _zsh_autopilot_metric_executed

  (( ZSH_AUTOPILOT_RECORD )) || return

  # A leading space under hist_ignore_space means "keep this out of history"
  # (e.g. `  export TOKEN=...`), avoiding a leak to a third-party LLM. Space
  # only, not tab; `emulate -L zsh` does not reset this option.
  [[ -o hist_ignore_space && $cmd == ' '* ]] && return

  _zsh_autopilot_send_record "$cmd" "$PWD" "$EPOCHSECONDS"
}

autoload -Uz add-zsh-hook

# add-zsh-hook runs hooks in registration order; this file's source position
# fixes that $?/git are fresh before _zsh_autopilot_precmd's next-command
# request fires.
add-zsh-hook precmd _zsh_autopilot_capture_exit
add-zsh-hook precmd _zsh_autopilot_refresh_git
add-zsh-hook chpwd _zsh_autopilot_refresh_git
add-zsh-hook precmd _zsh_autopilot_refresh_dir
add-zsh-hook chpwd _zsh_autopilot_refresh_dir
add-zsh-hook preexec _zsh_autopilot_record

# Seed the git cache immediately so context is sane even before the first
# precmd runs (e.g. a suggestion request triggered while typing on the very
# first prompt).
_zsh_autopilot_refresh_git

# Seed the directory-listing cache immediately, same reasoning as the git
# cache above.
_zsh_autopilot_refresh_dir

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

#--------------------------------------------------------------------#
# Start                                                              #
#--------------------------------------------------------------------#

_zsh_autopilot_start() {
  # Re-binds on every precmd so we stay wrapped around other plugins (e.g.
  # zsh-syntax-highlighting) and pick up widget-list changes. Costs
  # performance; ZSH_AUTOPILOT_MANUAL_REBIND disables it.
  if (( ${+ZSH_AUTOPILOT_MANUAL_REBIND} )); then
    add-zsh-hook -d precmd _zsh_autopilot_start
  fi

  _zsh_autopilot_bind_widgets
}

autoload -Uz add-zsh-hook

add-zsh-hook precmd _zsh_autopilot_start
add-zsh-hook precmd _zsh_autopilot_precmd

# Open the warm socket now so the first prompt already has a connection.
_zsh_autopilot_connect

#--------------------------------------------------------------------#
# Background self-update (TEMPORARY, dogfooding only)                #
#--------------------------------------------------------------------#

# At most once per ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL seconds, forks a
# detached job re-running the install script; a real update stops the
# daemon so the next terminal lazy-spawns the new binary. TEMPORARY: remove.

zmodload zsh/datetime 2>/dev/null

# Compares the installed VERSION stamp to what was last announced and queues
# an `updated` notice on a real version bump. Runs unconditionally (subject
# only to the notices gate) so the throttle below can't silence it.
_zsh_autopilot_announce_update() {
  emulate -L zsh

  local dir="${XDG_DATA_HOME:-$HOME/.local/share}/zsh-autopilot"
  local version_file="$dir/VERSION" announced_file="$dir/.announced-version"

  [[ -r $version_file ]] || return
  local version=$(<$version_file)

  if [[ ! -r $announced_file ]]; then
    print -r -- "$version" >| "$announced_file" 2>/dev/null
    return
  fi

  local announced=$(<$announced_file)
  [[ $version != $announced ]] && _zsh_autopilot_notice updated "updated $announced -> $version"
  print -r -- "$version" >| "$announced_file" 2>/dev/null
}

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

_zsh_autopilot_announce_update
_zsh_autopilot_autoupdate

#--------------------------------------------------------------------#
# CLI                                                                 #
#--------------------------------------------------------------------#

# User-facing `autopilot` command. Not an alias to autopilotd: version,
# log tailing, kill+respawn, and env-based level toggling are all shell-side
# operations the daemon binary has no flags for.

_zsh_autopilot_log_path() {
  print -r -- "${XDG_STATE_HOME:-$HOME/.local/state}/autopilot/daemon.log"
}

_zsh_autopilot_version_path() {
  print -r -- "${XDG_DATA_HOME:-$HOME/.local/share}/zsh-autopilot/VERSION"
}

# Kills the running daemon and respawns it, waiting out both transitions
# instead of racing them. `log-level` reuses this too since there's no
# config reload. Returns 1 if the daemon never comes back reachable.
_zsh_autopilot_restart_daemon() {
  emulate -L zsh
  zmodload zsh/net/socket 2>/dev/null

  pkill -x autopilotd 2>/dev/null

  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && exec {ZSH_AUTOPILOT_SOCKET_FD}<&- 2>/dev/null
  unset ZSH_AUTOPILOT_SOCKET_FD
  # Otherwise the once-per-shell spawn latch would skip this respawn.
  unset _ZSH_AUTOPILOT_SPAWN_TRIED

  local -i tries=0
  while (( tries++ < 20 )) && pgrep -x autopilotd >/dev/null 2>&1; do
    sleep 0.05
  done

  if ! _zsh_autopilot_spawn_daemon; then
    print -u2 -- "autopilot: daemon binary not found, can't restart"
    return 1
  fi

  tries=0
  while (( tries++ < 20 )); do
    if zsocket $ZSH_AUTOPILOT_SOCKET 2>/dev/null; then
      exec {REPLY}<&- 2>/dev/null
      return 0
    fi
    sleep 0.05
  done
  print -u2 -- "autopilot: daemon did not come back up; check $(_zsh_autopilot_log_path)"
  return 1
}

_zsh_autopilot_usage() {
  print -- "usage: autopilot {version|status|logs|restart|log-level info|debug}"
}

autopilot() {
  emulate -L zsh
  zmodload zsh/net/socket 2>/dev/null

  case "$1" in
    version)
      local f=$(_zsh_autopilot_version_path)
      if [[ -r $f ]]; then
        cat -- "$f"
      else
        print -u2 -- "autopilot: no VERSION file at $f (only written by install.sh)"
        return 1
      fi
      ;;
    status)
      if zsocket $ZSH_AUTOPILOT_SOCKET 2>/dev/null; then
        exec {REPLY}<&- 2>/dev/null
        print -- "autopilot: daemon reachable on $ZSH_AUTOPILOT_SOCKET"
      else
        print -- "autopilot: daemon not reachable on $ZSH_AUTOPILOT_SOCKET"
        return 1
      fi
      ;;
    logs)
      local f=$(_zsh_autopilot_log_path)
      if [[ -r $f ]]; then
        tail -f -- "$f"
      else
        print -u2 -- "autopilot: no log file at $f yet"
        return 1
      fi
      ;;
    restart)
      _zsh_autopilot_restart_daemon && print -- "autopilot: daemon restarted"
      ;;
    log-level)
      case "$2" in
        debug) export ZSH_AUTOPILOT_DEBUG=1 ;;
        info)  export ZSH_AUTOPILOT_DEBUG=0 ;;
        *) _zsh_autopilot_usage; return 1 ;;
      esac
      _zsh_autopilot_restart_daemon &&
        print -- "autopilot: log level set to $2 (daemon restarted, applies to every shell)"
      ;;
    help|-h|--help|"")
      _zsh_autopilot_usage
      ;;
    *)
      _zsh_autopilot_usage >&2
      return 1
      ;;
  esac
}

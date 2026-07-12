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
  whence -w _zsh_autopilot_send &>/dev/null && _zsh_autopilot_send "$BUFFER"
  return 0
}

# Offer a suggestion. Invoked as `zle autopilot-suggest -- "$suggestion"` by the
# socket transport when the daemon's reply arrives. This is the seam where the
# daemon's string becomes ghost text.
_zsh_autopilot_suggest() {
  emulate -L zsh

  local suggestion="$1"

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
  # Add the suggestion to the buffer
  BUFFER="$BUFFER$POSTDISPLAY"

  # Remove the suggestion
  POSTDISPLAY=

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

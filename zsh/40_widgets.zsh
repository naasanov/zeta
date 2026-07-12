
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

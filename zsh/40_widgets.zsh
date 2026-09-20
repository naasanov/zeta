
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

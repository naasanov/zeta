
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

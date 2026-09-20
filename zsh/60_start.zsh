
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

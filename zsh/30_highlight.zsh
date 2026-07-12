
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

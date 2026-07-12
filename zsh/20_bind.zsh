
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

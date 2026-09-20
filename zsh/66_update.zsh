
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


#--------------------------------------------------------------------#
# Background self-update (TEMPORARY — dogfooding only)                #
#--------------------------------------------------------------------#
# While dogfooding we want friends to pick up new releases without re-running
# the installer by hand. At shell startup — at most once per
# ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL seconds — fork a fully-detached job that
# re-runs the published install script. That script is version-aware: it exits
# immediately when already on the latest release, and on a real update it swaps
# the binary/bundle and stops the running daemon so the NEXT new terminal
# lazy-spawns the new one; the pkill drops this shell's socket fd too, so its
# very next request reconnects and spawns the new binary itself.
#
# Non-blocking by construction: the foreground shell never waits on the network
# (the whole check is backgrounded). The throttle keeps many terminals from
# hammering GitHub's unauthenticated API rate limit.
#
# Remove this fragment and its config block in 10_config.zsh before release —
# real distribution updates go through brew / plugin managers, not curl|sh on
# startup.

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

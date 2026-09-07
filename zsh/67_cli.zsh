
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
# instead of racing them. A level change needs a live daemon to pick it up,
# since there's no config reload, so `log-level` reuses this too. Returns 1
# if the daemon never comes back reachable.
_zsh_autopilot_restart_daemon() {
  emulate -L zsh
  zmodload zsh/net/socket 2>/dev/null

  pkill -x autopilotd 2>/dev/null

  [[ -n $ZSH_AUTOPILOT_SOCKET_FD ]] && exec {ZSH_AUTOPILOT_SOCKET_FD}<&- 2>/dev/null
  unset ZSH_AUTOPILOT_SOCKET_FD
  # Once-per-shell latch (50_socket.zsh) would otherwise skip this respawn.
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

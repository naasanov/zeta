
#--------------------------------------------------------------------#
# Context Capture (cwd / git / last exit / recent history)           #
#--------------------------------------------------------------------#
# Cheap, hook-driven context the socket transport (50_socket.zsh) rides along
# on every request. Nothing here runs per-keystroke: git state is computed on
# precmd/chpwd only and cached in globals; `_zsh_autopilot_send` just reads
# the cache. Running `git` on every keystroke would reintroduce the
# fork/exec-per-request cost this whole daemon architecture exists to avoid
# (design §7).

zmodload zsh/datetime 2>/dev/null

# Cached context globals. Empty/zero/false are the "nothing to report"
# values that the socket transport uses to omit a field entirely.
typeset -gi _ZSH_AUTOPILOT_LAST_EXIT=0
typeset -g _ZSH_AUTOPILOT_GIT_BRANCH=
typeset -g _ZSH_AUTOPILOT_GIT_DIRTY=false
typeset -ga _ZSH_AUTOPILOT_DIR_ENTRIES

# precmd hook: capture the previous command's exit status. This MUST be the
# very first statement of this function (and this function should be
# registered as early as possible in the precmd chain) so nothing — not even
# a harmless-looking builtin — clobbers $? before we read it.
_zsh_autopilot_capture_exit() {
  typeset -gi _ZSH_AUTOPILOT_LAST_EXIT=$?
}

# precmd + chpwd hook: refresh the cached git branch/dirty state. Two cheap
# git invocations, but only ever on a fresh prompt or directory change — never
# in the per-keystroke send path.
_zsh_autopilot_refresh_git() {
  emulate -L zsh

  typeset -g _ZSH_AUTOPILOT_GIT_BRANCH=
  typeset -g _ZSH_AUTOPILOT_GIT_DIRTY=false

  local branch
  # No branch (not a repo, or detached with no symbolic ref) -> leave the
  # cache cleared; the socket transport treats an empty branch as "not a repo"
  # and omits both git_branch and git_dirty.
  branch=$(git symbolic-ref --quiet --short HEAD 2>/dev/null) || return
  _ZSH_AUTOPILOT_GIT_BRANCH=$branch

  [[ -n $(git status --porcelain --untracked-files=no 2>/dev/null) ]] &&
    _ZSH_AUTOPILOT_GIT_DIRTY=true
}

# precmd + chpwd hook: refresh the cached listing of the current directory's
# entries. Cheap glob, no fork — but only ever on a fresh prompt or directory
# change, same cadence as git state above. precmd (not just chpwd) is
# intentional: it also catches files created within the directory (e.g. after
# `touch foo`).
_zsh_autopilot_refresh_dir() {
  emulate -L zsh

  typeset -g _ZSH_AUTOPILOT_DIR_ENTRIES=()

  local -a e
  e=( *(N) )

  (( ${#e} >= 1 && ${#e} <= 50 )) && _ZSH_AUTOPILOT_DIR_ENTRIES=("${e[@]}")
}

# preexec hook: report the about-to-run command to the daemon's history store.
# $1 is the raw line as typed, needed unexpanded for hist_ignore_space. $PWD
# here is the dir the command runs IN, so `cd ..` is tagged with the pre-cd
# directory.
_zsh_autopilot_record() {
  emulate -L zsh

  local cmd="$1"
  [[ -z $cmd ]] && return

  # METRICS(§12): signal that a previously accepted suggestion actually ran.
  whence -w _zsh_autopilot_metric_executed &>/dev/null && _zsh_autopilot_metric_executed

  (( ZSH_AUTOPILOT_RECORD )) || return

  # A leading space under hist_ignore_space means "keep this out of history"
  # (e.g. `  export TOKEN=...`) — recording it would leak it to a third-party
  # LLM. Space only, not tab; `emulate -L zsh` does not reset this option.
  [[ -o hist_ignore_space && $cmd == ' '* ]] && return

  _zsh_autopilot_send_record "$cmd" "$PWD" "$EPOCHSECONDS"
}

autoload -Uz add-zsh-hook

# Registered here (fragment 55, before 60_start.zsh's precmd hooks) so that
# by the time _zsh_autopilot_precmd (the next-command request) fires, $?/git
# are already fresh. add-zsh-hook runs hooks in registration order, and
# source order across the numbered fragments is what fixes that order.
add-zsh-hook precmd _zsh_autopilot_capture_exit
add-zsh-hook precmd _zsh_autopilot_refresh_git
add-zsh-hook chpwd _zsh_autopilot_refresh_git
add-zsh-hook precmd _zsh_autopilot_refresh_dir
add-zsh-hook chpwd _zsh_autopilot_refresh_dir
add-zsh-hook preexec _zsh_autopilot_record

# Seed the git cache immediately so context is sane even before the first
# precmd runs (e.g. a suggestion request triggered while typing on the very
# first prompt).
_zsh_autopilot_refresh_git

# Seed the directory-listing cache immediately, same reasoning as the git
# cache above.
_zsh_autopilot_refresh_dir

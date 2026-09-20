
#--------------------------------------------------------------------#
# Context Capture (cwd / git / last exit / recent history)           #
#--------------------------------------------------------------------#

# Git/dir state is cached from precmd/chpwd hooks, never computed per
# keystroke; running git on every keystroke would reintroduce the
# fork-per-request cost the daemon exists to avoid.

zmodload zsh/datetime 2>/dev/null

# Cached context globals. Empty/zero/false are the "nothing to report"
# values that the socket transport uses to omit a field entirely.
typeset -gi _ZSH_AUTOPILOT_LAST_EXIT=0
typeset -g _ZSH_AUTOPILOT_GIT_BRANCH=
typeset -g _ZSH_AUTOPILOT_GIT_DIRTY=false
typeset -ga _ZSH_AUTOPILOT_DIR_ENTRIES

# precmd hook: captures the previous command's exit status. Must be the very
# first statement here, and this hook must run first in the precmd chain, so
# nothing clobbers $? before it's read.
_zsh_autopilot_capture_exit() {
  typeset -gi _ZSH_AUTOPILOT_LAST_EXIT=$?
}

# precmd + chpwd hook: refreshes the cached git branch/dirty state. Two cheap
# git invocations, but only on a fresh prompt or directory change, never in
# the per-keystroke send path.
_zsh_autopilot_refresh_git() {
  emulate -L zsh

  typeset -g _ZSH_AUTOPILOT_GIT_BRANCH=
  typeset -g _ZSH_AUTOPILOT_GIT_DIRTY=false

  local branch
  # No branch (not a repo, or detached HEAD) leaves the cache cleared; an
  # empty git_branch means "not a repo" downstream.
  branch=$(git symbolic-ref --quiet --short HEAD 2>/dev/null) || return
  _ZSH_AUTOPILOT_GIT_BRANCH=$branch

  [[ -n $(git status --porcelain --untracked-files=no 2>/dev/null) ]] &&
    _ZSH_AUTOPILOT_GIT_DIRTY=true
}

# precmd + chpwd hook: refreshes the cached directory listing. precmd (not
# just chpwd) is required so files created in the current dir (e.g. after
# `touch foo`) still show up.
_zsh_autopilot_refresh_dir() {
  emulate -L zsh

  typeset -g _ZSH_AUTOPILOT_DIR_ENTRIES=()

  local -a e
  e=( *(N) )

  (( ${#e} >= 1 && ${#e} <= 50 )) && _ZSH_AUTOPILOT_DIR_ENTRIES=("${e[@]}")
}

# preexec hook: reports the about-to-run command to the daemon's history
# store. $1 is unexpanded (needed for hist_ignore_space); $PWD is the dir the
# command runs IN, so `cd ..` is tagged with the pre-cd directory.
_zsh_autopilot_record() {
  emulate -L zsh

  local cmd="$1"
  [[ -z $cmd ]] && return

  # METRICS(§12): signal that a previously accepted suggestion actually ran.
  whence -w _zsh_autopilot_metric_executed &>/dev/null && _zsh_autopilot_metric_executed

  (( ZSH_AUTOPILOT_RECORD )) || return

  # A leading space under hist_ignore_space means "keep this out of history"
  # (e.g. `  export TOKEN=...`), avoiding a leak to a third-party LLM. Space
  # only, not tab; `emulate -L zsh` does not reset this option.
  [[ -o hist_ignore_space && $cmd == ' '* ]] && return

  _zsh_autopilot_send_record "$cmd" "$PWD" "$EPOCHSECONDS"
}

autoload -Uz add-zsh-hook

# add-zsh-hook runs hooks in registration order; this file's source position
# fixes that $?/git are fresh before _zsh_autopilot_precmd's next-command
# request fires.
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

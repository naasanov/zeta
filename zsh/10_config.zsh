
#--------------------------------------------------------------------#
# Global Configuration Variables                                     #
#--------------------------------------------------------------------#

# Color to use when highlighting suggestion
# Uses format of `region_highlight`
# More info: http://zsh.sourceforge.net/Doc/Release/Zsh-Line-Editor.html#Zle-Widgets
(( ! ${+ZSH_AUTOPILOT_HIGHLIGHT_STYLE} )) &&
typeset -g ZSH_AUTOPILOT_HIGHLIGHT_STYLE='fg=8'

# Prefix to use when saving original versions of bound widgets
(( ! ${+ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX} )) &&
typeset -g ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX=autopilot-orig-

# Path to the autopilotd (or Phase 0 echo-server) Unix socket. Client and
# daemon must agree; matches the echo-server default so no -socket flag is
# needed. Kept short deliberately: macOS caps socket paths at ~104 bytes.
(( ! ${+ZSH_AUTOPILOT_SOCKET} )) &&
typeset -g ZSH_AUTOPILOT_SOCKET=/tmp/zsh-autopilot.sock

# Daemon binary to lazy-spawn (must be on $PATH) when the socket isn't up —
# see 50_socket.zsh's _zsh_autopilot_spawn_daemon. Set to empty to disable
# autostart and rely on the daemon being launched some other way (a launchd/
# systemd unit, the VS Code debug launch, manual `autopilotd &`).
(( ! ${+ZSH_AUTOPILOT_DAEMON_BIN} )) &&
typeset -g ZSH_AUTOPILOT_DAEMON_BIN=autopilotd

# TEMPORARY (dogfooding): background self-update on shell startup — see
# zsh/66_update.zsh. AUTOUPDATE=0 disables it; INTERVAL throttles how often the
# check may run (seconds; 0 = every shell). URL is the install script it re-runs
# (which no-ops when already on the latest release). Remove this block and the
# 66_update.zsh fragment before release — real updates go via brew/plugin-manager.
(( ! ${+ZSH_AUTOPILOT_AUTOUPDATE} )) &&
typeset -g ZSH_AUTOPILOT_AUTOUPDATE=1
(( ! ${+ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL} )) &&
typeset -gi ZSH_AUTOPILOT_AUTOUPDATE_INTERVAL=14400
(( ! ${+ZSH_AUTOPILOT_INSTALL_URL} )) &&
typeset -g ZSH_AUTOPILOT_INSTALL_URL=https://raw.githubusercontent.com/naasanov/zeta/main/scripts/install.sh

# Key sequence bound to autopilot-flag (METRICS §12). Empty disables the
# default binding. ^Xf is unbound in the stock zsh emacs keymap (unlike
# Alt-f/^[f, which is forward-word).
(( ! ${+ZSH_AUTOPILOT_FLAG_KEY} )) &&
typeset -g ZSH_AUTOPILOT_FLAG_KEY='^Xf'

# Whether this shell reports the commands it runs to the daemon's history
# store (47_context.zsh's _zsh_autopilot_record). 0 stops this shell
# contributing — suggestions still work, but nothing run here is recorded or
# cwd-tagged. Useful for keeping one shell's sensitive work out of history.
(( ! ${+ZSH_AUTOPILOT_RECORD} )) &&
typeset -gi ZSH_AUTOPILOT_RECORD=1

# Whether the client prints diagnostic notices (auth failures, missing
# daemon binary, updates) to stderr above the next prompt. 0 disables.
(( ! ${+ZSH_AUTOPILOT_NOTICES} )) &&
typeset -g ZSH_AUTOPILOT_NOTICES=1

# Widgets that clear the suggestion
(( ! ${+ZSH_AUTOPILOT_CLEAR_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_CLEAR_WIDGETS
  ZSH_AUTOPILOT_CLEAR_WIDGETS=(
    history-search-forward
    history-search-backward
    history-beginning-search-forward
    history-beginning-search-backward
    history-beginning-search-forward-end
    history-beginning-search-backward-end
    history-substring-search-up
    history-substring-search-down
    up-line-or-beginning-search
    down-line-or-beginning-search
    up-line-or-history
    down-line-or-history
    accept-line
    copy-earlier-word
  )
}

# Widgets that accept the entire suggestion
(( ! ${+ZSH_AUTOPILOT_ACCEPT_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_ACCEPT_WIDGETS
  ZSH_AUTOPILOT_ACCEPT_WIDGETS=(
    forward-char
    end-of-line
    vi-forward-char
    vi-end-of-line
    vi-add-eol
  )
}

# Widgets that accept the entire suggestion and execute it
(( ! ${+ZSH_AUTOPILOT_EXECUTE_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_EXECUTE_WIDGETS
  ZSH_AUTOPILOT_EXECUTE_WIDGETS=(
  )
}

# Widgets that accept the suggestion as far as the cursor moves
(( ! ${+ZSH_AUTOPILOT_PARTIAL_ACCEPT_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_PARTIAL_ACCEPT_WIDGETS
  ZSH_AUTOPILOT_PARTIAL_ACCEPT_WIDGETS=(
    forward-word
    emacs-forward-word
    vi-forward-word
    vi-forward-word-end
    vi-forward-blank-word
    vi-forward-blank-word-end
    vi-find-next-char
    vi-find-next-char-skip
  )
}

# Widgets that should be ignored (globbing supported but must be escaped)
(( ! ${+ZSH_AUTOPILOT_IGNORE_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_IGNORE_WIDGETS
  ZSH_AUTOPILOT_IGNORE_WIDGETS=(
    orig-\*
    beep
    run-help
    set-local-history
    which-command
    yank
    yank-pop
    zle-\*
  )
}

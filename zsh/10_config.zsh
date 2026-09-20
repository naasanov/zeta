
#--------------------------------------------------------------------#
# Global Configuration Variables                                     #
#--------------------------------------------------------------------#

# region_highlight-style color spec, e.g. 'fg=8'.
(( ! ${+ZSH_AUTOPILOT_HIGHLIGHT_STYLE} )) &&
typeset -g ZSH_AUTOPILOT_HIGHLIGHT_STYLE='fg=8'

(( ! ${+ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX} )) &&
typeset -g ZSH_AUTOPILOT_ORIGINAL_WIDGET_PREFIX=autopilot-orig-

# macOS caps Unix socket paths at ~104 bytes; keep this short.
(( ! ${+ZSH_AUTOPILOT_SOCKET} )) &&
typeset -g ZSH_AUTOPILOT_SOCKET=/tmp/zsh-autopilot.sock

# Daemon binary to lazy-spawn when the socket isn't up.
# Empty disables autostart; something else must launch the daemon then.
(( ! ${+ZSH_AUTOPILOT_DAEMON_BIN} )) &&
typeset -g ZSH_AUTOPILOT_DAEMON_BIN=autopilotd

# TEMPORARY (dogfooding): background self-update on shell startup.
# AUTOUPDATE=0 disables it; INTERVAL throttles the check (seconds, 0 = every shell).
# Remove this block and 66_update.zsh before release.
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

# Whether this shell reports commands to the daemon's history store.
# 0 keeps this shell's runs out of history entirely (suggestions still work).
(( ! ${+ZSH_AUTOPILOT_RECORD} )) &&
typeset -gi ZSH_AUTOPILOT_RECORD=1

# Whether the client prints diagnostic notices (auth failures, missing
# daemon binary, updates) to stderr above the next prompt. 0 disables.
(( ! ${+ZSH_AUTOPILOT_NOTICES} )) &&
typeset -g ZSH_AUTOPILOT_NOTICES=1

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

(( ! ${+ZSH_AUTOPILOT_EXECUTE_WIDGETS} )) && {
  typeset -ga ZSH_AUTOPILOT_EXECUTE_WIDGETS
  ZSH_AUTOPILOT_EXECUTE_WIDGETS=(
  )
}

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

# Entries may be globs; a literal `*` must be escaped (e.g. `orig-\*`).
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

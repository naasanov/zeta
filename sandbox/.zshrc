# sandbox/.zshrc — isolated zsh config for developing the zsh-autopilot client.
#
# This runs when zsh is launched with ZDOTDIR pointing at this folder, e.g. the
# "sandbox: fresh zsh" VSCode task, or from a shell:
#
#     ZDOTDIR=sandbox zsh
#
# Because ZDOTDIR redirects where zsh looks for user rc files, NONE of your real
# ~/.zshrc, oh-my-zsh, or zsh-autosuggestions loads here. Nothing else fights
# the plugin for POSTDISPLAY / ghost text, so what you see is purely our code.
# Type `exit` to drop back to your normal shell, fully intact.

# Keep sandbox history out of your real ~/.zsh_history.
HISTFILE="${ZDOTDIR}/.zsh_history"
HISTSIZE=1000
SAVEHIST=1000

# Load developer env vars (GROQ_API_KEY, any ZSH_AUTOPILOT_* overrides) from an
# untracked sandbox/.env if present, before the plugin reads its config. `set -a`
# exports each assignment so child processes see it. See sandbox/.env.example.
if [[ -r "${ZDOTDIR}/.env" ]]; then
  set -a
  source "${ZDOTDIR}/.env"
  set +a
fi

# Make it unmistakable that you're in the sandbox.
PROMPT='%F{cyan}[autopilot-sandbox]%f %1~ %# '

# Which plugin to load. Defaults to the real client entry point. Override with
# AUTOPILOT_PLUGIN to point at a different file.
: "${AUTOPILOT_PLUGIN:=${ZDOTDIR}/../zsh-autopilot.plugin.zsh}"

if [[ -r "$AUTOPILOT_PLUGIN" ]]; then
  source "$AUTOPILOT_PLUGIN"
else
  print -u2 "sandbox: plugin not found: $AUTOPILOT_PLUGIN"
fi

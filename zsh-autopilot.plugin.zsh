# zsh-autopilot.plugin.zsh: plugin entry point, sourced by plugin managers
# (oh-my-zsh, zinit, antidote, zplug). Kept tiny: resolves paths and hands
# off to the zsh client in zsh/.

# Resolve the directory this plugin lives in, regardless of how it was sourced.
0="${${ZERO:-${0:#$ZSH_ARGZERO}}:-${(%):-%N}}"
ZSH_AUTOPILOT_DIR="${0:A:h}"

# Source the generated bundle (built from zsh/*.zsh by `make plugin`).
source "${ZSH_AUTOPILOT_DIR}/zsh-autopilot.zsh"

unset ZSH_AUTOPILOT_DIR

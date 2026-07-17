#!/bin/sh
# zsh-autopilot install script — for friends dogfooding, not a public release.
#
# Downloads the autopilotd daemon binary from the latest GitHub Release
# (built by GoReleaser, see ../.goreleaser.yaml) plus the zsh plugin bundle,
# installs them locally, and prints the .zshrc lines to add by hand.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/naasanov/zeta/main/scripts/install.sh | sh
#
# Optional: set ZSH_AUTOPILOT_INSTALL_KEY to have the Codestral key line
# printed pre-filled instead of a placeholder.

set -eu

REPO="naasanov/zeta"
BIN_DIR="${HOME}/.local/bin"
SHARE_DIR="${HOME}/.local/share/zsh-autopilot"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "error: unsupported arch: $arch" >&2; exit 1 ;;
esac
case "$os" in
  darwin|linux) ;;
  *) echo "error: unsupported OS: $os" >&2; exit 1 ;;
esac

echo "==> Fetching latest release info for ${REPO}..."
latest_tag="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
if [ -z "$latest_tag" ]; then
  echo "error: could not determine latest release tag" >&2
  exit 1
fi
version="${latest_tag#v}"
asset="zsh-autopilot_${version}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${latest_tag}/${asset}"

# Rerunnable: record the installed release tag and skip the work when we're
# already on the latest. `--force` (or ZSH_AUTOPILOT_INSTALL_FORCE=1) reinstalls
# anyway. The background self-updater (zsh/66_update.zsh) relies on this early
# exit to be a cheap no-op on the common "already current" path.
version_file="${SHARE_DIR}/VERSION"
installed=""
[ -f "$version_file" ] && installed="$(cat "$version_file" 2>/dev/null)"
force="${ZSH_AUTOPILOT_INSTALL_FORCE:-}"
[ "${1:-}" = "--force" ] && force=1
if [ -n "$installed" ] && [ "$installed" = "$latest_tag" ] && [ -z "$force" ]; then
  echo "==> Already up to date (${latest_tag})."
  exit 0
fi

echo "==> Downloading ${asset} (${latest_tag})..."
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
curl -fsSL "$url" -o "${tmp_dir}/${asset}"
tar -xzf "${tmp_dir}/${asset}" -C "$tmp_dir" autopilotd

mkdir -p "$BIN_DIR"
mv "${tmp_dir}/autopilotd" "${BIN_DIR}/autopilotd"
chmod +x "${BIN_DIR}/autopilotd"
echo "==> Installed daemon: ${BIN_DIR}/autopilotd"

echo "==> Fetching zsh plugin bundle (${latest_tag})..."
mkdir -p "$SHARE_DIR"
raw_base="https://raw.githubusercontent.com/${REPO}/${latest_tag}"
curl -fsSL "${raw_base}/zsh-autopilot.plugin.zsh" -o "${SHARE_DIR}/zsh-autopilot.plugin.zsh"
curl -fsSL "${raw_base}/zsh-autopilot.zsh" -o "${SHARE_DIR}/zsh-autopilot.zsh"
echo "==> Installed plugin: ${SHARE_DIR}/zsh-autopilot.plugin.zsh"

# Record what we just installed so a rerun can detect "already current".
printf '%s\n' "$latest_tag" > "$version_file"

# We just swapped the daemon binary; stop any running daemon so the next shell
# that needs it lazy-spawns the new one (a no-op if none is running). The
# single-instance guard means the old one must exit before the new can bind.
pkill -x autopilotd 2>/dev/null || true

# On an update (a prior version existed) the .zshrc is already wired — just
# report. On a first install, print the lines the user still has to add.
if [ -n "$installed" ]; then
  echo "==> Updated ${installed} -> ${latest_tag}. Open a new terminal to pick it up."
  exit 0
fi

key_line="export ZSH_AUTOPILOT_CODESTRAL_KEY=\"${ZSH_AUTOPILOT_INSTALL_KEY:-<PASTE_KEY_HERE>}\""

cat <<EOF

==> Add these lines to your ~/.zshrc:

# zsh-autopilot
export PATH="${BIN_DIR}:\$PATH"
export ZSH_AUTOPILOT_PROVIDER=codestral
${key_line}
source "${SHARE_DIR}/zsh-autopilot.plugin.zsh"

Reload your shell and start typing.
EOF

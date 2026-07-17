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

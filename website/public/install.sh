#!/bin/sh
# install.sh — Install the rat binary.
# Usage: curl -fsSL https://runanything.dev/install.sh | sh
#
# Installs to ~/.local/bin (no root required).
# Works on macOS (Intel + Apple Silicon) and Linux (x86_64 + arm64).

set -eu

REPO="maximerivest/rat"
INSTALL_DIR="${RAT_INSTALL_DIR:-$HOME/.local/bin}"
BASE_URL="https://github.com/${REPO}/releases/latest/download"

# ── Detect platform ──────────────────────────────────────────

OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
  Linux)  PLATFORM="linux" ;;
  Darwin) PLATFORM="darwin" ;;
  *)      echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)             echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

BINARY="rat-${PLATFORM}-${ARCH}"
URL="${BASE_URL}/${BINARY}"

# ── Download ─────────────────────────────────────────────────

echo "Downloading rat from ${URL}..."
mkdir -p "$INSTALL_DIR"

if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$URL" -o "${INSTALL_DIR}/rat"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "${INSTALL_DIR}/rat" "$URL"
else
  echo "Error: curl or wget required" >&2
  exit 1
fi

chmod +x "${INSTALL_DIR}/rat"

# ── macOS: strip quarantine xattr ────────────────────────────

if [ "$PLATFORM" = "darwin" ]; then
  xattr -d com.apple.quarantine "${INSTALL_DIR}/rat" 2>/dev/null || true
fi

# ── Ensure INSTALL_DIR is on PATH ────────────────────────────

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;; # already on PATH
  *)
    echo ""
    echo "Add ${INSTALL_DIR} to your PATH:"
    echo ""

    SHELL_NAME="$(basename "${SHELL:-/bin/sh}")"
    case "$SHELL_NAME" in
      zsh)  RC="$HOME/.zshrc" ;;
      bash) RC="$HOME/.bashrc" ;;
      fish)
        echo "  set -Ux fish_user_paths ${INSTALL_DIR} \$fish_user_paths"
        echo ""
        echo "Then restart your shell."
        echo ""
        echo "Installed rat to ${INSTALL_DIR}/rat"
        exit 0
        ;;
      *)    RC="$HOME/.profile" ;;
    esac

    echo "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ${RC}"
    echo ""
    echo "Then restart your shell or run:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac

echo ""
echo "Installed rat to ${INSTALL_DIR}/rat"
echo ""
echo "Get started:"
echo "  rat install py"
echo "  rat py"

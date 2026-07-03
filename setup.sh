#!/bin/bash
set -euo pipefail

# Parse flags
NO_DAEMON=false
for arg in "$@"; do
  case "$arg" in
    --no-daemon) NO_DAEMON=true ;;
    --help|-h)
      echo "Usage: ./setup.sh [--no-daemon]"
      echo "  --no-daemon  Skip launchd daemon installation"
      exit 0
      ;;
  esac
done

STEPS=5
if [ "$NO_DAEMON" = true ]; then
  STEPS=4
fi

echo "=== orch setup ==="

# 1. Go binary
echo "[1/$STEPS] Building orch binary..."
VERSION="${ORCH_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "dev")}"
go build -ldflags "-X main.version=${VERSION}" -o orch ./cmd/orch/
sudo cp orch /usr/local/bin/orch
rm -f orch
echo "   ✅ /usr/local/bin/orch (${VERSION})"

# 2. Config
echo "[2/$STEPS] Setting up config..."
CONFIG_DIR="$HOME/.config/orch"
mkdir -p "$CONFIG_DIR"
if [ ! -f "$CONFIG_DIR/config.yaml" ]; then
  cp config.yaml "$CONFIG_DIR/config.yaml"
  echo "   ✅ config copied to $CONFIG_DIR/config.yaml"
else
  echo "   ⏭️  config already exists, skipping"
fi

# 3. MLX env
if [ ! -d "$HOME/mlx-env" ]; then
  echo "[3/$STEPS] Setting up MLX environment..."
  python3 -m venv "$HOME/mlx-env"
  "$HOME/mlx-env/bin/pip" install mlx-lm
  echo "   ✅ ~/mlx-env ready"
else
  echo "[3/$STEPS] ⏭️  ~/mlx-env already exists"
fi

# 4. Pre-download model
echo "[4/$STEPS] Pre-downloading model (if needed)..."
"$HOME/mlx-env/bin/python3" -c "from mlx_lm import load; load('mlx-community/Qwen2.5-1.5B-Instruct-4bit')" 2>/dev/null || true
echo "   ✅ model cached"

# 5. LaunchAgent (MLX server daemon)
if [ "$NO_DAEMON" = false ]; then
  echo "[5/$STEPS] Installing MLX server LaunchAgent..."
  LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
  PLIST_NAME="com.orch.mlx-server.plist"
  PLIST_SRC="./launchd/$PLIST_NAME"
  PLIST_DEST="$LAUNCH_AGENTS_DIR/$PLIST_NAME"

  mkdir -p "$LAUNCH_AGENTS_DIR"

  # Replace __HOME__ placeholder with actual $HOME path
  sed "s|__HOME__|$HOME|g" "$PLIST_SRC" > "$PLIST_DEST"

  # Unload first if already loaded (ignore errors)
  launchctl unload "$PLIST_DEST" 2>/dev/null || true

  # Load the agent
  launchctl load "$PLIST_DEST"
  echo "   ✅ LaunchAgent installed and loaded"
  echo "   📋 Log: ~/Library/Logs/orch-mlx.log"
  echo "   🔧 Manage: launchctl unload ~/Library/LaunchAgents/$PLIST_NAME"
fi

echo ""
echo "=== setup complete ==="
echo "Run: orch \"hello\""
if [ "$NO_DAEMON" = false ]; then
  echo "MLX server running on http://localhost:8080 (auto-restarts on crash)"
fi

#!/bin/bash
# orch setup — 新機器前置安裝腳本
# 適用 macOS (Apple Silicon)
set -e

echo "🔧 orch setup — 安裝前置環境"
echo ""

# 1. Go
if ! command -v go &>/dev/null; then
    echo "📦 安裝 Go..."
    brew install go
else
    echo "✅ Go $(go version | awk '{print $3}')"
fi

# 2. MLX LM（用於本地推理 server）
MLX_ENV="$HOME/mlx-env"
if [ ! -f "$MLX_ENV/bin/python3" ]; then
    echo "📦 建立 MLX Python venv..."
    python3 -m venv "$MLX_ENV"
    source "$MLX_ENV/bin/activate"
    pip install --quiet mlx-lm
    deactivate
else
    echo "✅ MLX venv ($MLX_ENV)"
fi

# 3. 下載模型
MODEL="mlx-community/Qwen2.5-3B-Instruct-4bit"
echo "📦 確認 MLX 模型 ($MODEL)..."
source "$MLX_ENV/bin/activate"
python3 -c "
from mlx_lm import load
load('$MODEL')
print('  ✅ model ready')
" 2>/dev/null
deactivate

# 4. Build orch
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
echo "📦 Build orch..."
cd "$SCRIPT_DIR"
go build -o orch ./cmd/orch/

# 5. 安裝到 PATH
INSTALL_DIR="$HOME/go/bin"
mkdir -p "$INSTALL_DIR"
cp orch "$INSTALL_DIR/orch"
rm -f orch
echo "✅ orch 安裝至 $INSTALL_DIR/orch"

# 6. 建立 mlx server 啟動腳本
MLX_SERVER="$INSTALL_DIR/orch-server"
cat > "$MLX_SERVER" << EOF
#!/bin/bash
source "$MLX_ENV/bin/activate"
exec mlx_lm.server --model "$MODEL" --port 8080
EOF
chmod +x "$MLX_SERVER"
echo "✅ orch-server 安裝至 $MLX_SERVER"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🏁 安裝完成！"
echo ""
echo "使用方式："
echo "  1. 先啟動本地 LLM server："
echo "     orch-server"
echo ""
echo "  2. 然後用 orch："
echo "     orch \"你的任務\"     # oneshot"
echo "     orch               # REPL"
echo "     orch --tools       # 查看可用工具"
echo ""
echo "前提："
echo "  - ~/go/bin 在 PATH 中"
echo "  - 確保 kiro-cli / claude 等 agent 已安裝"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

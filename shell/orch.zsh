# orch shell integration
# 加到 ~/.zshrc：source ~/Desktop/Cowork/Study/projects/multi-agent-orchestrator/orch/shell/orch.zsh

# ═══════════════════════════════════════════════════════════════
# Aliases — 常用流程一鍵觸發
# ═══════════════════════════════════════════════════════════════

alias om='orch "開工"'          # morning / 開工
alias os='orch "收工"'          # signoff / 收工
alias ow='orch "週報"'          # weekly report
alias ost='orch "status"'       # quick status check
alias ov='orch "交給 Victoria"' # handoff to Victoria

# 進 session mode：orch 沒有直接跳進 session 的啟動旗標，/session 是 REPL 內部指令，
# 不能用 shell `&&` 接續執行——進 orch 後手動輸入 /session claude 或 /session kiro

# ═══════════════════════════════════════════════════════════════
# Zsh Completion
# ═══════════════════════════════════════════════════════════════

_orch() {
  local -a commands subcommands

  # Top-level subcommands
  commands=(
    'history:查看/搜尋任務歷史'
    'briefing:查看/設定每日簡報'
    'init:互動式設定精靈'
  )

  # Flags
  local -a flags
  flags=(
    '--tools:顯示可用工具'
    '--dry-run:只規劃不執行'
    '--verbose:顯示 MLX 除錯輸出'
    '--backend:指定 AI backend (kiro/claude/gemini)'
    '--version:顯示版本'
    '--help:顯示說明'
  )

  if (( CURRENT == 2 )); then
    # First argument — could be subcommand, flag, or prompt
    _describe 'command' commands
    _describe 'flag' flags
  elif (( CURRENT == 3 )); then
    case "${words[2]}" in
      history)
        local -a history_sub
        history_sub=('search:搜尋歷史' 'clear:清除歷史')
        _describe 'subcommand' history_sub
        ;;
      briefing)
        local -a briefing_sub
        briefing_sub=('set:手動設定' 'gen:自動生成')
        _describe 'subcommand' briefing_sub
        ;;
      --backend)
        local -a backends
        backends=('kiro' 'claude' 'gemini')
        _describe 'backend' backends
        ;;
    esac
  fi
}

compdef _orch orch

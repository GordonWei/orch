// Package workflow 提供 YAML 工作流模板系統。
// 使用者可在 ~/.config/orch/workflows/ 定義可重複使用的多步驟工作流，
// 透過觸發關鍵字自動執行，跳過 planner 的 AI 規劃階段。
package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/planner"
	"gopkg.in/yaml.v3"
)

// WorkflowStep 定義工作流中的單一步驟
type WorkflowStep struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Agent       string   `yaml:"agent"`
	Prompt      string   `yaml:"prompt,omitempty"`
	Command     string   `yaml:"command,omitempty"`
	VerifyCmd   string   `yaml:"verify_cmd,omitempty"`
	DependsOn   []string `yaml:"depends_on,omitempty"`
	OnFailure   string   `yaml:"on_failure,omitempty"`
}

// Workflow 定義一個完整的工作流模板
type Workflow struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Trigger     string            `yaml:"trigger"`
	Variables   map[string]string `yaml:"variables,omitempty"`
	Steps       []WorkflowStep    `yaml:"steps"`
}

// LoadAll 載入指定目錄下所有 .yaml/.yml 工作流檔案
func LoadAll(dir string) ([]Workflow, error) {
	dir = expandHome(dir)

	// 目錄不存在時回傳空切片（不視為錯誤）
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var workflows []Workflow
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue // 跳過讀取失敗的檔案
		}

		var w Workflow
		if err := yaml.Unmarshal(data, &w); err != nil {
			continue // 跳過格式錯誤的檔案
		}

		// 至少要有 trigger 和 steps 才算有效
		if w.Trigger != "" && len(w.Steps) > 0 {
			workflows = append(workflows, w)
		}
	}

	return workflows, nil
}

// Match 根據使用者輸入比對工作流觸發關鍵字
// 回傳第一個匹配的工作流，若無匹配回傳 nil
func Match(input string, workflows []Workflow) *Workflow {
	inputLower := strings.ToLower(strings.TrimSpace(input))

	for i := range workflows {
		triggerLower := strings.ToLower(strings.TrimSpace(workflows[i].Trigger))
		if triggerLower == "" {
			continue
		}

		// 完全匹配或輸入包含觸發詞
		if inputLower == triggerLower || strings.Contains(inputLower, triggerLower) {
			return &workflows[i]
		}
	}

	return nil
}

// ToPlanner 將 Workflow 轉換為 planner.Plan 以交由 executor 執行
// vars 為使用者自定義變數，會與內建變數合併後進行模板替換
func ToPlanner(w *Workflow, vars map[string]string, cfg *config.Config) *planner.Plan {
	// 合併內建變數
	allVars := builtinVars(cfg)
	// 加入工作流本身定義的預設變數
	for k, v := range w.Variables {
		allVars[k] = v
	}
	// 使用者提供的變數優先覆蓋
	for k, v := range vars {
		allVars[k] = v
	}

	steps := make([]planner.Step, 0, len(w.Steps))
	for _, ws := range w.Steps {
		// 模板替換 prompt 和 command
		prompt := renderTemplate(ws.Prompt, allVars)
		command := renderTemplate(ws.Command, allVars)
		description := renderTemplate(ws.Description, allVars)

		step := planner.Step{
			ID:          ws.ID,
			Description: description,
			Agent:       ws.Agent,
			Prompt:      prompt,
			Command:     command,
			VerifyCmd:   ws.VerifyCmd,
			DependsOn:   ws.DependsOn,
			OnFailure:   ws.OnFailure,
		}
		steps = append(steps, step)
	}

	return &planner.Plan{
		TaskSummary: w.Description,
		Difficulty:  "workflow",
		Category:    "workflow",
		Steps:       steps,
	}
}

// builtinVars 回傳內建模板變數
func builtinVars(cfg *config.Config) map[string]string {
	now := time.Now()
	vars := map[string]string{
		"date": now.Format("2006-01-02"),
		"time": now.Format("15:04:05"),
	}

	// 從 config 取得使用者名稱
	if cfg != nil && cfg.Persona.Owner != "" {
		vars["user"] = cfg.Persona.Owner
	}

	return vars
}

// renderTemplate 執行 Go 模板替換
// 若模板解析或渲染失敗，回傳原始字串
func renderTemplate(text string, vars map[string]string) string {
	if text == "" {
		return ""
	}

	// 快速檢查：沒有模板語法就直接回傳
	if !strings.Contains(text, "{{") {
		return text
	}

	tmpl, err := template.New("wf").Parse(text)
	if err != nil {
		return text
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, vars); err != nil {
		return text
	}

	return buf.String()
}

// expandHome 展開 ~ 前綴路徑
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

package registry

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
)

type Tool struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Available   bool     `json:"available"`
	Model       string   `json:"model,omitempty"`
	Strengths   []string `json:"strengths"`
	Description string   `json:"description"`
}

type Registry struct {
	Tools []Tool `json:"tools"`
}

func Scan() *Registry {
	tools := []toolDef{
		{
			name:        "kiro",
			binary:      "kiro-cli",
			strengths:   []string{"code", "infra", "aws", "gcp", "terraform", "deploy", "build", "test", "file-ops", "shell"},
			description: "AI coding agent with MCP tools (Notion, AWS, filesystem)",
			modelCmd:    []string{"kiro-cli"},
		},
		{
			name:        "claude",
			binary:      "claude",
			strengths:   []string{"notion", "gcal", "gmail", "google-workspace", "writing", "analysis", "meeting-notes"},
			description: "Claude Code with Google Workspace + Notion MCP",
			modelCmd:    []string{"claude"},
		},
		{
			name:        "gemini",
			binary:      "gemini",
			strengths:   []string{"long-context", "video", "image", "summarization", "google-drive"},
			description: "Google Gemini CLI for long-context analysis and multimodal",
			modelCmd:    nil,
		},
		{
			name:        "terraform",
			binary:      "terraform",
			strengths:   []string{"iac", "plan", "apply", "state"},
			description: "Infrastructure as Code",
			modelCmd:    nil,
		},
		{
			name:        "kubectl",
			binary:      "kubectl",
			strengths:   []string{"kubernetes", "pods", "deploy", "logs", "exec"},
			description: "Kubernetes cluster management",
			modelCmd:    nil,
		},
		{
			name:        "helm",
			binary:      "helm",
			strengths:   []string{"helm", "chart", "release", "values"},
			description: "Kubernetes package manager",
			modelCmd:    nil,
		},
		{
			name:        "aws",
			binary:      "aws",
			strengths:   []string{"aws-api", "s3", "ec2", "lambda", "iam", "cloudformation"},
			description: "AWS CLI",
			modelCmd:    nil,
		},
		{
			name:        "gcloud",
			binary:      "gcloud",
			strengths:   []string{"gcp-api", "gke", "cloud-run", "iam", "compute"},
			description: "Google Cloud CLI",
			modelCmd:    nil,
		},
	}

	reg := &Registry{}
	for _, td := range tools {
		t := Tool{
			Name:        td.name,
			Strengths:   td.strengths,
			Description: td.description,
		}

		path, err := exec.LookPath(td.binary)
		if err == nil {
			t.Available = true
			t.Path = path
			if td.modelCmd != nil {
				t.Model = detectModel(td.modelCmd)
			}
		}

		reg.Tools = append(reg.Tools, t)
	}

	return reg
}

func (r *Registry) Available() []Tool {
	var out []Tool
	for _, t := range r.Tools {
		if t.Available {
			out = append(out, t)
		}
	}
	return out
}

func (r *Registry) ToJSON() string {
	b, _ := json.MarshalIndent(r.Available(), "", "  ")
	return string(b)
}

func (r *Registry) Summary() string {
	var parts []string
	for _, t := range r.Available() {
		model := ""
		if t.Model != "" {
			model = " (model: " + t.Model + ")"
		}
		parts = append(parts, "- "+t.Name+model+": "+strings.Join(t.Strengths, ", "))
	}
	return strings.Join(parts, "\n")
}

type toolDef struct {
	name        string
	binary      string
	strengths   []string
	description string
	modelCmd    []string
}

func detectModel(cmd []string) string {
	// Read config to detect model, don't call CLI (avoid CLI treating command as prompt)
	switch cmd[0] {
	case "claude":
		return readJSONField(os.Getenv("HOME")+"/.claude/settings.json", "model")
	case "kiro-cli":
		// kiro model is dynamically selected by server
		return "auto"
	}
	return ""
}

func readJSONField(path, field string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if v, ok := m[field]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

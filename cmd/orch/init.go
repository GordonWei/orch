package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gordonwei/orch/pkg/backend"
)

// handleInit runs the interactive initialization wizard.
// Detects environment, asks preferences, writes config.
func handleInit() {
	fmt.Println("🚀 orch init — interactive setup")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// Step 1: Detect backends
	fmt.Println("📡 Detecting AI backends...")
	available := backend.DetectBackends()
	if len(available) == 0 {
		fmt.Println("   ⚠️  No AI CLI backends found.")
		fmt.Println("   Install one of: kiro-cli, claude, or gemini")
		fmt.Println("   orch will still work for keyword shortcuts and MLX local inference.")
	} else {
		for _, name := range available {
			fmt.Printf("   ✅ %s\n", name)
		}
	}
	fmt.Println()

	// Step 2: Choose primary backend
	var primary string
	if len(available) == 1 {
		primary = available[0]
		fmt.Printf("🎯 Primary backend: %s (only one detected)\n", primary)
	} else if len(available) > 1 {
		fmt.Printf("🎯 Choose primary AI backend [%s] (default: %s): ", strings.Join(available, "/"), available[0])
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if choice == "" {
			primary = available[0]
		} else {
			primary = choice
		}
	}
	fmt.Println()

	// Step 3: Language
	fmt.Print("🌐 Preferred language (en/zh-TW/ja, default: en): ")
	lang, _ := reader.ReadString('\n')
	lang = strings.TrimSpace(lang)
	if lang == "" {
		lang = "en"
	}

	// Step 4: Owner name (optional)
	fmt.Print("👤 Your name (optional, for personalized prompts): ")
	owner, _ := reader.ReadString('\n')
	owner = strings.TrimSpace(owner)

	// Step 5: MLX local model (required on Apple Silicon)
	fmt.Println("🍎 MLX local inference (required — Apple Silicon primary engine)")
	fmt.Print("   Model [mlx-community/Qwen2.5-1.5B-Instruct-4bit]: ")
	mlxModel, _ := reader.ReadString('\n')
	mlxModel = strings.TrimSpace(mlxModel)
	if mlxModel == "" {
		mlxModel = "mlx-community/Qwen2.5-1.5B-Instruct-4bit"
	}
	enableMLX := true

	// Step 6: Workspace root (optional)
	fmt.Print("📂 Workspace root directory (optional, e.g., ~/projects): ")
	workspace, _ := reader.ReadString('\n')
	workspace = strings.TrimSpace(workspace)

	fmt.Println()
	fmt.Println("📝 Writing config...")

	// Generate config
	configContent := generateConfig(primary, lang, owner, enableMLX, workspace, mlxModel)

	// Ensure directory exists
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".config", "orch")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to create config dir: %v\n", err)
		os.Exit(1)
	}

	// Write config
	configPath := filepath.Join(configDir, "config.yaml")
	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("   ⚠️  %s already exists. Overwrite? (y/N): ", configPath)
		overwrite, _ := reader.ReadString('\n')
		overwrite = strings.TrimSpace(strings.ToLower(overwrite))
		if overwrite != "y" && overwrite != "yes" {
			fmt.Println("   (skipped)")
			fmt.Println()
			fmt.Println("✅ Done! You can edit the config manually at:")
			fmt.Printf("   %s\n", configPath)
			return
		}
	}

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "❌ failed to write config: %v\n", err)
		os.Exit(1)
	}

	// Create workflows directory
	workflowsDir := filepath.Join(configDir, "workflows")
	os.MkdirAll(workflowsDir, 0755)

	fmt.Printf("   ✅ config written to %s\n", configPath)
	fmt.Println()
	fmt.Println("🎉 Setup complete! Next steps:")
	fmt.Println("   1. Ensure MLX environment is ready:")
	fmt.Println("      make install   (or ./setup.sh)")
	fmt.Println("   2. Try it:")
	fmt.Println("      orch \"hello\"")
}

func generateConfig(primary, lang, owner string, enableMLX bool, workspace string, mlxModel string) string {
	// Build system prompt based on language
	systemPrompt := "You are orch, an AI task orchestration CLI running on macOS (Apple Silicon).\nYou coordinate AI agents to complete tasks efficiently.\n\nResponse rules:\n- Be concise and direct\n- Keep technical terms in English"
	if lang == "zh-TW" {
		systemPrompt = "You are orch, an AI task orchestration CLI running on macOS (Apple Silicon).\nYou coordinate AI agents to complete tasks efficiently.\n\nResponse rules:\n- Reply in Traditional Chinese (zh-TW)\n- Be concise and direct\n- Keep technical terms in English (Kubernetes, Terraform, GKE, etc.)\n- Keep code and commands in English"
	}

	ownerLine := ""
	if owner != "" {
		ownerLine = fmt.Sprintf("  owner: %q\n", owner)
	}

	primaryLine := ""
	if primary != "" {
		primaryLine = fmt.Sprintf("  primary: %q\n", primary)
	}

	mlxSection := `# MLX local inference disabled
# Uncomment to enable:
# models:
#   - name: "qwen-1.5b"
#     backend: "mlx"
#     endpoint: "http://localhost:8080"
#     model: "mlx-community/Qwen2.5-1.5B-Instruct-4bit"
#     python_path: "~/mlx-env/bin/python3"
#     auto_start: true
#     port: "8080"
#     default: true`

	if enableMLX {
		mlxSection = fmt.Sprintf(`models:
  - name: "mlx-default"
    backend: "mlx"
    endpoint: "http://localhost:8080"
    model: "%s"
    python_path: "~/mlx-env/bin/python3"
    auto_start: true
    port: "8080"
    default: true`, mlxModel)
	}

	workspaceLine := ""
	if workspace != "" {
		workspaceLine = fmt.Sprintf("\nworkspace:\n  root: %q\n", workspace)
	}

	return fmt.Sprintf(`# orch config — generated by 'orch init'
# Location: ~/.config/orch/config.yaml
# Override with ORCH_CONFIG environment variable

persona:
  name: "orch"
%s  language: %q
  system_prompt: |
    %s

ai_backend:
%s
%s

memory:
  db_path: "~/.config/orch/orch.db"
  briefing_on_boot: true
  auto_summarize: true

workflows:
  dir: "~/.config/orch/workflows"
%s
routing:
  kiro: [code, infra, aws, gcp, terraform, deploy, build, test, file-ops, shell]
  claude: [notion, gcal, gmail, google-workspace, writing, analysis, meeting-notes]
  gemini: [long-context, video, image, summarization, google-drive]

keyword_shortcuts:
  - prefix: "kubectl"
    category: infra
  - prefix: "terraform plan"
    category: infra
  - prefix: "terraform apply"
    category: infra
  - prefix: "helm upgrade"
    category: deploy
  - prefix: "helm install"
    category: deploy
  - prefix: "aws s3"
    category: query
  - prefix: "aws ec2"
    category: query
  - prefix: "gcloud compute"
    category: query
  - prefix: "gcloud container"
    category: query
`, ownerLine, lang, strings.ReplaceAll(systemPrompt, "\n", "\n    "), primaryLine, mlxSection, workspaceLine)
}

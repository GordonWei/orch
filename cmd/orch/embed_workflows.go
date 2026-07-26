package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed workflows/*.yaml
var embeddedWorkflows embed.FS

// copyWorkflowTemplates copies bundled workflow templates into the user's
// workflows directory. Only copies files that don't already exist (won't
// overwrite user customizations).
func copyWorkflowTemplates(workflowsDir string) (copied int, err error) {
	entries, err := embeddedWorkflows.ReadDir("workflows")
	if err != nil {
		return 0, fmt.Errorf("read embedded workflows: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		destPath := filepath.Join(workflowsDir, entry.Name())

		// Don't overwrite existing files
		if _, err := os.Stat(destPath); err == nil {
			continue
		}

		content, err := embeddedWorkflows.ReadFile("workflows/" + entry.Name())
		if err != nil {
			return copied, fmt.Errorf("read %s: %w", entry.Name(), err)
		}

		if err := os.WriteFile(destPath, content, 0644); err != nil {
			return copied, fmt.Errorf("write %s: %w", entry.Name(), err)
		}
		copied++
	}

	return copied, nil
}

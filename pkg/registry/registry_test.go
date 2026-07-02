package registry

import (
	"testing"
)

func TestScan(t *testing.T) {
	reg := Scan()

	if len(reg.Tools) == 0 {
		t.Fatal("expected at least some tools defined")
	}

	// At least some tools should be available (local machine has bash)
	// But which ones are available depends on environment, so only check structure
	for _, tool := range reg.Tools {
		if tool.Name == "" {
			t.Error("tool with empty name")
		}
		if len(tool.Strengths) == 0 {
			t.Errorf("tool %q has no strengths", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
	}
}

func TestAvailable(t *testing.T) {
	reg := Scan()
	avail := reg.Available()

	for _, tool := range avail {
		if !tool.Available {
			t.Errorf("Available() returned tool %q with Available=false", tool.Name)
		}
		if tool.Path == "" {
			t.Errorf("Available tool %q has empty path", tool.Name)
		}
	}
}

func TestToJSON(t *testing.T) {
	reg := Scan()
	json := reg.ToJSON()

	if json == "" {
		t.Fatal("ToJSON returned empty string")
	}
	if json[0] != '[' {
		t.Errorf("expected JSON array, got: %c...", json[0])
	}
}

func TestSummary(t *testing.T) {
	reg := Scan()
	summary := reg.Summary()

	if summary == "" {
		t.Fatal("Summary returned empty string")
	}
}

func TestReadJSONField(t *testing.T) {
	// Test non-existent file
	result := readJSONField("/nonexistent/path.json", "model")
	if result != "" {
		t.Errorf("expected empty for nonexistent file, got %q", result)
	}
}

package registry

import (
	"testing"
)

func TestScan(t *testing.T) {
	reg := Scan()

	if len(reg.Tools) == 0 {
		t.Fatal("expected at least some tools defined")
	}

	// 至少應該有一些工具是 available 的（本機有 bash）
	// 但具體哪些可用取決於環境，所以只檢查結構
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
	// 測試不存在的檔案
	result := readJSONField("/nonexistent/path.json", "model")
	if result != "" {
		t.Errorf("expected empty for nonexistent file, got %q", result)
	}
}

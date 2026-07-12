package router

import (
	"sync"
	"testing"

	"github.com/gordonwei/orch/pkg/config"
	"github.com/gordonwei/orch/pkg/session"
)

// testRouter creates a Router with default route rules for testing.
func testRouter() *Router {
	return New(config.Load().RouteRules)
}

// testRouterWithCooldown creates a Router with a specific cooldown.
func testRouterWithCooldown(cooldown int) *Router {
	cfg := config.Load().RouteRules
	cfg.Cooldown = cooldown
	return New(cfg)
}

// ══════════════════════════════════════════════════════════════════════════════
// Classify tests
// ══════════════════════════════════════════════════════════════════════════════

func TestClassify_CLICommands(t *testing.T) {
	r := testRouter()

	cases := []struct {
		name  string
		input string
	}{
		{"kubectl", "kubectl get pods -n production"},
		{"kubectl_short", "kubectl apply -f deployment.yaml"},
		{"k_alias", "k get svc"},
		{"terraform", "terraform plan -var-file=prod.tfvars"},
		{"tf_alias", "tf apply"},
		{"git", "git status"},
		{"git_push", "git push origin main"},
		{"ls", "ls -la /var/log"},
		{"cat", "cat /etc/hosts"},
		{"grep", "grep -r 'error' ./logs/"},
		{"docker", "docker ps -a"},
		{"docker_compose", "docker-compose up -d"},
		{"helm", "helm list -A"},
		{"aws", "aws s3 ls"},
		{"gcloud", "gcloud compute instances list"},
		{"npm", "npm install express"},
		{"make", "make build"},
		{"cargo", "cargo test"},
		{"go", "go build ./..."},
		{"pip", "pip install -r requirements.txt"},
		{"curl", "curl -s https://api.example.com/health"},
		{"ssh", "ssh user@server.example.com"},
		{"brew", "brew install jq"},
		{"find", "find . -name '*.go' -type f"},
		{"ps", "ps aux | grep nginx"},
		{"df", "df -h"},
		{"echo", "echo $HOME"},
		{"whoami", "whoami"},
		{"chmod", "chmod +x deploy.sh"},
		{"mkdir", "mkdir -p output/logs"},
		{"sudo_prefix", "sudo systemctl restart nginx"},
		{"path_prefix", "./deploy.sh --env=prod"},
		{"absolute_path", "/usr/local/bin/myapp"},
		{"sam", "sam deploy --guided"},
		{"pulumi", "pulumi up"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Classify(tc.input)
			if got != ClassCommand {
				t.Errorf("Classify(%q) = %v, want ClassCommand", tc.input, got)
			}
		})
	}
}

func TestClassify_Chat(t *testing.T) {
	r := testRouter()

	cases := []struct {
		name  string
		input string
	}{
		// Chinese greetings
		{"chinese_hello", "你好"},
		{"chinese_hi", "嗨"},
		{"chinese_hello_2", "哈囉"},
		{"chinese_morning", "早安"},
		{"chinese_afternoon", "午安"},
		{"chinese_evening", "晚安"},
		{"chinese_thanks", "謝謝"},
		{"chinese_bye", "再見"},
		{"chinese_bye_2", "掰掰"},
		{"chinese_who", "你是誰"},

		// English greetings
		{"english_hello", "hello"},
		{"english_hi", "hi"},
		{"english_hey", "hey there"},
		{"english_thanks", "thank you"},
		{"english_bye", "bye"},
		{"english_goodbye", "goodbye"},
		{"english_morning", "good morning"},
		{"english_night", "good night"},
		{"english_who", "who are you"},
		{"english_introduce", "introduce yourself"},

		// Short inputs (default to chat)
		{"short_ok", "ok"},
		{"short_yes", "yes"},
		{"short_no", "no"},
		{"short_hmm", "hmm"},

		// Emoji (short → chat)
		{"emoji_thumbs_up", "👍"},
		{"emoji_smile", "😊"},
		{"emoji_heart", "❤️"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Classify(tc.input)
			if got != ClassChat {
				t.Errorf("Classify(%q) = %v, want ClassChat", tc.input, got)
			}
		})
	}
}

func TestClassify_NaturalLanguage(t *testing.T) {
	r := testRouter()

	cases := []struct {
		name  string
		input string
	}{
		// English sentences
		{"english_sentence", "help me deploy the application to production"},
		{"english_question", "how do I configure the load balancer for our GKE cluster"},
		{"english_task", "summarize the meeting notes from yesterday and push to Notion"},
		{"english_debug", "locate the bug in the authentication middleware"},

		// Chinese tech questions
		{"chinese_s3", "幫我查 S3 bucket 的使用量"},
		{"chinese_deploy", "部署 litellm 到 GKE"},
		{"chinese_kubectl", "幫我看一下 kubectl pod 狀態"},
		{"chinese_terraform", "用 terraform 建立新的 VPC"},
		{"chinese_meeting", "整理今天三場會議記錄到 Notion"},
		{"chinese_monitor", "監控 production 環境的 CPU 使用率"},
		{"chinese_backup", "備份 PostgreSQL 資料庫"},
		{"chinese_fix", "修正 API gateway 的 timeout 問題"},

		// Mixed language
		{"mixed_check", "check 一下 S3 bucket 的 policy"},
		{"mixed_setup", "set up CI/CD pipeline for the new service"},
		{"mixed_analyze", "analyze the CloudWatch logs for errors in the last hour"},

		// Contains tech keywords (override chat-like length)
		{"tech_short", "fix the docker build"},
		{"tech_refactor", "refactor the lambda handler"},
		{"tech_debug", "debug the kubernetes ingress"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Classify(tc.input)
			if got != ClassNaturalLanguage {
				t.Errorf("Classify(%q) = %v, want ClassNaturalLanguage", tc.input, got)
			}
		})
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Hint tests
// ══════════════════════════════════════════════════════════════════════════════

func TestHint_CrossDomain(t *testing.T) {
	cases := []struct {
		name           string
		input          string
		currentBackend session.Backend
		wantSuggested  session.Backend
		wantKeyword    string
	}{
		{
			name:           "notion_keyword_in_kiro_session",
			input:          "同步 notion 上的資料",
			currentBackend: session.BackendKiro,
			wantSuggested:  session.BackendClaude,
			wantKeyword:    "同步 notion",
		},
		{
			name:           "deploy_keyword_in_claude_session",
			input:          "幫我部署到 GKE",
			currentBackend: session.BackendClaude,
			wantSuggested:  session.BackendKiro,
			wantKeyword:    "部署",
		},
		{
			name:           "terraform_keyword_in_claude_session",
			input:          "run terraform plan",
			currentBackend: session.BackendClaude,
			wantSuggested:  session.BackendKiro,
			wantKeyword:    "terraform plan",
		},
		{
			name:           "meeting_notes_phrase_in_kiro_session",
			input:          "整理 meeting notes for today",
			currentBackend: session.BackendKiro,
			wantSuggested:  session.BackendClaude,
			wantKeyword:    "meeting notes",
		},
		{
			name:           "會議記錄_phrase_in_kiro_session",
			input:          "整理今天的會議記錄",
			currentBackend: session.BackendKiro,
			wantSuggested:  session.BackendClaude,
			wantKeyword:    "會議記錄",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRouterWithCooldown(0) // disable cooldown for testing
			hint := r.Hint(tc.input, tc.currentBackend)
			if hint.Suggested != tc.wantSuggested {
				t.Errorf("Hint(%q, %q).Suggested = %q, want %q",
					tc.input, tc.currentBackend, hint.Suggested, tc.wantSuggested)
			}
			if hint.Keyword != tc.wantKeyword {
				t.Errorf("Hint(%q, %q).Keyword = %q, want %q",
					tc.input, tc.currentBackend, hint.Keyword, tc.wantKeyword)
			}
		})
	}
}

func TestHint_SameDomain(t *testing.T) {
	cases := []struct {
		name           string
		input          string
		currentBackend session.Backend
	}{
		{
			name:           "terraform_in_kiro",
			input:          "run terraform plan",
			currentBackend: session.BackendKiro,
		},
		{
			name:           "notion_in_claude",
			input:          "update the notion page",
			currentBackend: session.BackendClaude,
		},
		{
			name:           "deploy_in_kiro",
			input:          "deploy the new version",
			currentBackend: session.BackendKiro,
		},
		{
			name:           "meeting_in_claude",
			input:          "整理會議記錄",
			currentBackend: session.BackendClaude,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRouterWithCooldown(0)
			hint := r.Hint(tc.input, tc.currentBackend)
			if hint.Suggested != "" {
				t.Errorf("Hint(%q, %q).Suggested = %q, want empty (no cross-domain hint)",
					tc.input, tc.currentBackend, hint.Suggested)
			}
		})
	}
}

func TestHint_Cooldown(t *testing.T) {
	r := testRouterWithCooldown(3) // cooldown = 3 inputs between hints

	// First hint should fire
	hint1 := r.Hint("update notion page", session.BackendKiro)
	if hint1.Suggested == "" {
		t.Fatal("first hint should fire, got empty")
	}

	// Next calls within cooldown should be suppressed
	hint2 := r.Hint("sync to notion", session.BackendKiro)
	if hint2.Suggested != "" {
		t.Errorf("second hint within cooldown should be suppressed, got: %+v", hint2)
	}

	hint3 := r.Hint("notion meeting notes", session.BackendKiro)
	if hint3.Suggested != "" {
		t.Errorf("third hint within cooldown should be suppressed, got: %+v", hint3)
	}

	// After cooldown expires (3 inputs later), hint should fire again
	// inputCounter is now 3 (from the 3 Hint calls above), lastHintAt=1
	// We need inputCounter - lastHintAt >= cooldown (3), so one more call
	hint4 := r.Hint("push to notion", session.BackendKiro)
	if hint4.Suggested == "" {
		t.Error("hint after cooldown should fire, got empty")
	}
}

func TestHint_StrengthFilter(t *testing.T) {
	// Weak signals (strength 1) should NOT trigger hints

	// "會議" and "筆記" are strength 1 for claude
	cases := []struct {
		name  string
		input string
	}{
		{"會議_weak", "今天有會議"},
		{"build_weak", "build something"},
		{"test_weak", "test the feature"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := testRouterWithCooldown(0)
			hint := rt.Hint(tc.input, session.BackendKiro)
			// Strength 1 rules should NOT trigger hints (Hint filters for >= 2)
			if hint.Suggested != "" {
				t.Errorf("Hint(%q) fired with strength %d, should be filtered (min 2)",
					tc.input, hint.Strength)
			}
		})
	}
}

func TestHint_PhraseBeatsKeyword(t *testing.T) {
	r := testRouterWithCooldown(0)

	// "terraform plan" phrase (strength 3 for kiro) should win over
	// potential weaker matches.
	hint := r.Hint("terraform plan for the new vpc", session.BackendClaude)
	if hint.Suggested != session.BackendKiro {
		t.Errorf("expected kiro suggestion for 'terraform plan', got %q", hint.Suggested)
	}
	if hint.Keyword != "terraform plan" {
		t.Errorf("expected keyword 'terraform plan', got %q", hint.Keyword)
	}
	if hint.Strength != 3 {
		t.Errorf("expected strength 3, got %d", hint.Strength)
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// SuggestBackend tests
// ══════════════════════════════════════════════════════════════════════════════

func TestSuggestBackend_Basic(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantBackend session.Backend
	}{
		{"notion_keyword", "update the notion page", session.BackendClaude},
		{"terraform_phrase", "terraform plan for vpc", session.BackendKiro},
		{"deploy_keyword", "deploy to production", session.BackendKiro},
		{"gcal_keyword", "check gcal for tomorrow", session.BackendClaude},
		{"kubectl_phrase", "kubectl get pods in staging", session.BackendKiro},
		{"會議記錄_phrase", "整理今天的會議記錄", session.BackendClaude},
		{"部署_keyword", "幫我部署新版本", session.BackendKiro},
		{"salesforce_keyword", "update salesforce opportunity", session.BackendClaude},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRouter()
			got, _ := r.SuggestBackend(tc.input)
			if got != tc.wantBackend {
				t.Errorf("SuggestBackend(%q) = %q, want %q", tc.input, got, tc.wantBackend)
			}
		})
	}
}

func TestSuggestBackend_HistoryMomentum(t *testing.T) {
	r := testRouter()

	// Build history momentum toward kiro
	r.RecordInput("kubectl get pods", session.BackendKiro)
	r.RecordInput("helm list", session.BackendKiro)
	r.RecordInput("docker ps", session.BackendKiro)

	// Ambiguous input (no strong keyword match) should get boosted toward kiro
	backend, _ := r.SuggestBackend("check the status")
	if backend != session.BackendKiro {
		t.Errorf("after kiro momentum, SuggestBackend('check the status') = %q, want %q",
			backend, session.BackendKiro)
	}
}

// TestSuggestBackend_HistoryMomentumDeterministicTie guards against a regression
// where historyMomentum() picked the "winning" backend via Go map iteration
// (non-deterministic order). With the default history_size (5), two backends
// can never both reach the momentum threshold (3) in the same window, so the
// bug was unreachable under default config — this test uses a larger
// history_size to force the tie and asserts the result is stable across many
// calls, not a coin flip per call.
func TestSuggestBackend_HistoryMomentumDeterministicTie(t *testing.T) {
	cfg := config.Load().RouteRules
	cfg.HistorySize = 10
	r := New(cfg)

	// Interleave so both backends independently cross the threshold (3) within
	// the same window; Kiro is first-seen and must win deterministically.
	r.RecordInput("kubectl get pods", session.BackendKiro)
	r.RecordInput("notion page", session.BackendClaude)
	r.RecordInput("helm list", session.BackendKiro)
	r.RecordInput("meeting notes", session.BackendClaude)
	r.RecordInput("docker ps", session.BackendKiro)
	r.RecordInput("交接", session.BackendClaude)

	for i := 0; i < 50; i++ {
		backend, _ := r.SuggestBackend("check the status")
		if backend != session.BackendKiro {
			t.Fatalf("iteration %d: SuggestBackend('check the status') = %q, want %q (deterministic first-seen tie-break, not random map order)",
				i, backend, session.BackendKiro)
		}
	}
}

func TestSuggestBackend_NoMatch(t *testing.T) {
	r := testRouter()

	// Input with no matching keywords at all
	backend, keyword := r.SuggestBackend("hello world")
	if backend != "" {
		t.Errorf("SuggestBackend('hello world') = (%q, %q), want empty", backend, keyword)
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// RecordInput tests
// ══════════════════════════════════════════════════════════════════════════════

func TestRecordInput_WindowSize(t *testing.T) {
	cfg := config.Load().RouteRules
	cfg.HistorySize = 3 // small window for testing
	r := New(cfg)

	// Add more entries than window size
	inputs := []string{"a", "b", "c", "d", "e"}
	for _, in := range inputs {
		r.RecordInput(in, session.BackendKiro)
	}

	r.mu.Lock()
	histLen := len(r.history)
	r.mu.Unlock()

	if histLen != 3 {
		t.Errorf("history length = %d, want 3 (window size)", histLen)
	}

	// Verify only the last 3 entries are retained
	r.mu.Lock()
	if r.history[0].Input != "c" || r.history[1].Input != "d" || r.history[2].Input != "e" {
		t.Errorf("history should contain [c,d,e], got [%s,%s,%s]",
			r.history[0].Input, r.history[1].Input, r.history[2].Input)
	}
	r.mu.Unlock()
}

func TestRecordInput_DefaultWindowSize(t *testing.T) {
	// HistorySize=0 should default to 5
	cfg := config.RouteRulesConfig{HistorySize: 0}
	r := New(cfg)

	for i := 0; i < 10; i++ {
		r.RecordInput("input", session.BackendKiro)
	}

	r.mu.Lock()
	histLen := len(r.history)
	r.mu.Unlock()

	if histLen != 5 {
		t.Errorf("default history window: length = %d, want 5", histLen)
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Thread safety test
// ══════════════════════════════════════════════════════════════════════════════

func TestRouter_ThreadSafety(t *testing.T) {
	r := testRouter()
	var wg sync.WaitGroup

	// Concurrent Hint calls
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Hint("terraform plan for vpc", session.BackendClaude)
		}()
	}

	// Concurrent RecordInput calls
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.RecordInput("kubectl get pods", session.BackendKiro)
		}()
	}

	// Concurrent SuggestBackend calls
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.SuggestBackend("deploy to production")
		}()
	}

	// Concurrent Classify calls (read-only, no lock, but tests overall safety)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Classify("kubectl get pods")
		}()
	}

	// Concurrent AutoRoute toggle
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.SetAutoRoute(true)
			_ = r.AutoRoute()
			r.SetAutoRoute(false)
		}()
	}

	wg.Wait()
	// If we reach here without panic/race detector complaints, the test passes.
}

// ══════════════════════════════════════════════════════════════════════════════
// AutoRoute tests
// ══════════════════════════════════════════════════════════════════════════════

func TestAutoRoute_Toggle(t *testing.T) {
	r := testRouter()

	if r.AutoRoute() {
		t.Error("AutoRoute should be false by default")
	}

	r.SetAutoRoute(true)
	if !r.AutoRoute() {
		t.Error("AutoRoute should be true after SetAutoRoute(true)")
	}

	r.SetAutoRoute(false)
	if r.AutoRoute() {
		t.Error("AutoRoute should be false after SetAutoRoute(false)")
	}
}

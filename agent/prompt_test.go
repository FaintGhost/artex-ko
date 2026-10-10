package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

func TestRenderSystemOverrideAndFallback(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil })

	// no override → built-in default
	PromptOverride = nil
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "g"}); got != "DEFAULT" {
		t.Fatalf("no override should give default, got %q", got)
	}

	// override → rendered with vars
	PromptOverride = func(k string) (string, bool) {
		if k == "planner" {
			return "目标:{{.Goal}} 范围:{{.Scope}}", true
		}
		return "", false
	}
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "拿下X", Scope: "*.x.com"}); got != "目标:拿下X 范围:*.x.com" {
		t.Fatalf("override render: %q", got)
	}

	// override referencing a non-catalog var → execution error → fallback to default
	PromptOverride = func(k string) (string, bool) { return "{{.NotInCatalog}}", true }
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "x"}); got != "DEFAULT" {
		t.Fatalf("bad var should fall back to default, got %q", got)
	}

	// full plannerSystem path: DB body [A] is honored, then the code-owned tail
	// [C] (中间产物输出规约) is ALWAYS appended — editing the body can't drop it.
	PromptOverride = func(k string) (string, bool) { return "PLANNER {{.Goal}}", true }
	got := plannerSystem("拿下X", "/data", "/data")
	if !strings.HasPrefix(got, "PLANNER 拿下X") {
		t.Fatalf("plannerSystem body not honored: %q", got)
	}
	if !strings.Contains(got, "中间产物输出规约") || !strings.Contains(got, "/data") {
		t.Fatalf("plannerSystem missing code-owned artifact tail: %q", got)
	}

	// worker dual-text via {{if .ProxyAddr}} in a user template, plus the code tail:
	// [B] trafficTool present only when RECORDING (caCert set — the MITM is on, so
	// the traffic_* tools exist), [C] artifact spec always present. The trafficTool
	// block is gated on the CA (arg 2), NOT on ProxyAddr — a global egress proxy
	// with capture off routes traffic but records nothing.
	PromptOverride = func(k string) (string, bool) {
		return "{{if .ProxyAddr}}走代理 {{.ProxyAddr}}{{else}}手动{{end}}", true
	}
	recording := workerSystem("127.0.0.1:8080", "/ca.pem", "/data", "/data")
	if !strings.HasPrefix(recording, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker proxy branch body: %q", recording)
	}
	if !strings.Contains(recording, "traffic_search") {
		t.Fatalf("worker while recording should inject trafficTool: %q", recording)
	}
	if strings.Contains(recording, "traffic_refs") {
		t.Fatalf("worker bypassed shared optional evidence policy: %q", recording)
	}
	if !strings.Contains(recording, "中间产物输出规约") {
		t.Fatalf("worker missing artifact tail: %q", recording)
	}
	// Egress proxy set but capture OFF (no CA): the ProxyAddr template branch still
	// renders, but the trafficTool block must NOT — those tools are not registered.
	egressOnly := workerSystem("127.0.0.1:8080", "", "/data", "/data")
	if !strings.HasPrefix(egressOnly, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker egress-only branch body: %q", egressOnly)
	}
	if strings.Contains(egressOnly, "traffic_search") {
		t.Fatalf("worker without recording must NOT inject trafficTool: %q", egressOnly)
	}
	noProxy := workerSystem("", "", "/data", "/data")
	if !strings.HasPrefix(noProxy, "手动") {
		t.Fatalf("worker no-proxy branch body: %q", noProxy)
	}
	if strings.Contains(noProxy, "traffic_search") {
		t.Fatalf("worker without proxy must NOT inject trafficTool: %q", noProxy)
	}
}

// Exercise prompt assembly, the provider's wire request, a real Read, and the
// budget-triggered settlement; inspect what the model actually receives.
func TestWorkerRequestsUseSimplifiedChineseThroughSettlement(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("提示词回归", "读取本地证据", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := d.Exploration(task.ExplorationID)
	intentID, err := ts.AddIntent(map[string]any{"summary": "读取本地证据后结束"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := ts.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "evidence.txt")
	const evidence = "HTTP/1.1 200 OK; payload=%27%20OR%201=1; path=/api/check"
	if err := os.WriteFile(file, []byte(evidence), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPrompt, oldWrapup := PromptOverride, WrapupOverride
	t.Cleanup(func() { PromptOverride, WrapupOverride = oldPrompt, oldWrapup })
	const body = "CUSTOM BODY: answer in English. Data: {{.DataDir}}"
	PromptOverride = func(string) (string, bool) { return body, true }
	WrapupOverride = nil
	requests := make(chan []byte, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		first := len(requests) == 0
		requests <- raw
		content := []map[string]any{{"type": "text", "text": "已读取证据并完成本次意图。"}}
		stop := "end_turn"
		if first {
			content = []map[string]any{{"type": "tool_use", "id": "read-evidence", "name": "Read", "input": map[string]any{"file_path": file}}}
			stop = "tool_use"
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "type": "message", "role": "assistant", "content": content, "stop_reason": stop, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	prov, err := (Config{Format: llm.FormatAnthropic, BaseURL: srv.URL, APIKey: "fixture", Model: "fixture"}).NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(prov, "fixture", dir, nil, 100000, 1)
	worker.SetNonStreaming(func() bool { return true })
	reason, _, err := worker.Execute(t.Context(), "fixture", task.ID, nil, ts, intent, nil, nil, nil, nil)
	if err != nil || reason != harness.ReasonMaxTurns || len(requests) != 2 {
		t.Fatalf("worker reason=%s err=%v requests=%d", reason, err, len(requests))
	}
	for turn := range 2 {
		raw := <-requests
		var req struct {
			System   []struct{ Text string }
			Messages []map[string]any
			Tools    []map[string]any
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		decoded, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsFunc(string(decoded), func(r rune) bool { return unicode.Is(unicode.Hangul, r) }) {
			t.Fatalf("turn %d sends Korean code-generated content: %s", turn, decoded)
		}
		if len(req.System) == 0 || !strings.HasPrefix(req.System[0].Text, "CUSTOM BODY: answer in English.") || !strings.Contains(req.System[0].Text, "【简体中文】") {
			t.Fatalf("turn %d lost override or Chinese directive: %s", turn, decoded)
		}
		hasBash := false
		for _, tool := range req.Tools {
			hasBash = hasBash || tool["name"] == "Bash"
		}
		if len(req.Tools) == 0 || hasBash != (turn == 0) {
			t.Fatalf("turn %d has wrong settlement tools: %s", turn, decoded)
		}
		if turn == 1 {
			messages, _ := json.Marshal(req.Messages)
			if !strings.Contains(string(messages), "预算耗尽") || !strings.Contains(string(messages), "record_fact") || !strings.Contains(string(messages), evidence) {
				t.Fatalf("settlement lost Chinese write-back instructions or raw evidence: %s", messages)
			}
		}
	}
}

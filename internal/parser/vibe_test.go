package parser

import (
	"path/filepath"
	"testing"
)

func TestDiscoverVibeSessions(t *testing.T) {
	dir := filepath.Join("testdata", "vibe")
	files := DiscoverVibeSessions(dir)

	if len(files) != 1 {
		t.Fatalf("expected 1 session, got %d", len(files))
	}

	if files[0].Agent != AgentVibe {
		t.Errorf("expected agent %s, got %s", AgentVibe, files[0].Agent)
	}

	if filepath.Base(files[0].Path) != "messages.jsonl" {
		t.Errorf("expected messages.jsonl, got %s", filepath.Base(files[0].Path))
	}
}

func TestDiscoverVibeSessions_EmptyDir(t *testing.T) {
	files := DiscoverVibeSessions("/nonexistent/path")
	if len(files) != 0 {
		t.Errorf("expected 0 files for nonexistent dir, got %d", len(files))
	}
}

func TestFindVibeSourceFile(t *testing.T) {
	dir, _ := filepath.Abs(filepath.Join("testdata", "vibe"))

	path := FindVibeSourceFile(dir, "session_001")
	if path == "" {
		t.Fatal("expected to find session file")
	}

	path = FindVibeSourceFile(dir, "nonexistent_session")
	if path != "" {
		t.Errorf("expected empty path for nonexistent session, got %s", path)
	}
}

func TestParseVibeSession(t *testing.T) {
	path := filepath.Join("testdata", "vibe",
		"session_001", "messages.jsonl")

	sess, msgs, err := ParseVibeSession(path, "test-machine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess == nil {
		t.Fatal("expected session, got nil")
	}

	// Session metadata from meta.json.
	if sess.ID != "vibe:7972424f-ecdb-4d2b-ab1a-add8b940114a" {
		t.Errorf("unexpected session ID: %s", sess.ID)
	}
	if sess.Agent != AgentVibe {
		t.Errorf("expected agent %s, got %s", AgentVibe, sess.Agent)
	}
	if sess.Project != "my_project" {
		t.Errorf("expected project my_project, got %s", sess.Project)
	}
	if sess.GitBranch != "main" {
		t.Errorf("expected branch main, got %s", sess.GitBranch)
	}
	if sess.Machine != "test-machine" {
		t.Errorf("expected machine test-machine, got %s", sess.Machine)
	}
	if sess.FirstMessage != "Fix the build error in main.go" {
		t.Errorf("unexpected first message: %s", sess.FirstMessage)
	}

	// Timestamps from meta.json.
	if sess.StartedAt.IsZero() {
		t.Error("expected non-zero StartedAt")
	}
	if sess.EndedAt.IsZero() {
		t.Error("expected non-zero EndedAt")
	}
	if !sess.EndedAt.After(sess.StartedAt) {
		t.Error("expected EndedAt after StartedAt")
	}

	// Messages: 1 user + 3 assistant + 2 tool = 6 total.
	if sess.MessageCount != 6 {
		t.Errorf("expected 6 messages, got %d", sess.MessageCount)
	}
	if sess.UserMessageCount != 1 {
		t.Errorf("expected 1 user message, got %d", sess.UserMessageCount)
	}

	// Verify message structure.
	if len(msgs) != 6 {
		t.Fatalf("expected 6 parsed messages, got %d", len(msgs))
	}

	// All messages should have timestamps set to session start time.
	for i, m := range msgs {
		if m.Timestamp.IsZero() {
			t.Errorf("msg[%d]: expected non-zero Timestamp", i)
		}
	}

	// First message: user.
	if msgs[0].Role != RoleUser {
		t.Errorf("msg[0]: expected user role, got %s", msgs[0].Role)
	}
	if msgs[0].Content != "Fix the build error in main.go" {
		t.Errorf("msg[0]: unexpected content: %s", msgs[0].Content)
	}

	// Second message: assistant with tool call.
	if msgs[1].Role != RoleAssistant {
		t.Errorf("msg[1]: expected assistant role, got %s", msgs[1].Role)
	}
	if !msgs[1].HasToolUse {
		t.Error("msg[1]: expected HasToolUse=true")
	}
	if len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("msg[1]: expected 1 tool call, got %d", len(msgs[1].ToolCalls))
	}
	if msgs[1].ToolCalls[0].ToolName != "bash" {
		t.Errorf("msg[1]: expected tool name bash, got %s", msgs[1].ToolCalls[0].ToolName)
	}
	if msgs[1].Model != "devstral-2" {
		t.Errorf("msg[1]: expected model devstral-2, got %s", msgs[1].Model)
	}

	// Third message: tool result.
	if len(msgs[2].ToolResults) != 1 {
		t.Fatalf("msg[2]: expected 1 tool result, got %d", len(msgs[2].ToolResults))
	}
	if msgs[2].ToolResults[0].ToolUseID != "tc-001" {
		t.Errorf("msg[2]: expected tool_use_id tc-001, got %s", msgs[2].ToolResults[0].ToolUseID)
	}

	// Last message: assistant without tool call, carries session tokens.
	if msgs[5].Role != RoleAssistant {
		t.Errorf("msg[5]: expected assistant role, got %s", msgs[5].Role)
	}
	if msgs[5].HasToolUse {
		t.Error("msg[5]: expected HasToolUse=false")
	}
	if msgs[5].ContextTokens != 22082 {
		t.Errorf("msg[5]: expected 22082 context tokens, got %d", msgs[5].ContextTokens)
	}
	if msgs[5].OutputTokens != 87 {
		t.Errorf("msg[5]: expected 87 output tokens, got %d", msgs[5].OutputTokens)
	}
	if !msgs[5].HasContextTokens {
		t.Error("msg[5]: expected HasContextTokens=true")
	}
	if !msgs[5].HasOutputTokens {
		t.Error("msg[5]: expected HasOutputTokens=true")
	}

	// Session-level token aggregates.
	if sess.TotalOutputTokens != 87 {
		t.Errorf("expected TotalOutputTokens=87, got %d", sess.TotalOutputTokens)
	}
	if sess.PeakContextTokens != 22082 {
		t.Errorf("expected PeakContextTokens=22082, got %d", sess.PeakContextTokens)
	}
}

func TestParseVibeSession_NoFile(t *testing.T) {
	sess, msgs, err := ParseVibeSession("/nonexistent/messages.jsonl", "m")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if sess != nil || msgs != nil {
		t.Error("expected nil session and messages for missing file")
	}
}

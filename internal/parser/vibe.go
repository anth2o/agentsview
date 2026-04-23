// ABOUTME: Parses Mistral Vibe CLI session files into structured session data.
// ABOUTME: Vibe stores sessions as directories under ~/.vibe/logs/session/ with
// ABOUTME: a meta.json metadata file and a messages.jsonl conversation log.
package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// vibeMeta holds fields from the session's meta.json file.
type vibeMeta struct {
	SessionID   string `json:"session_id"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	GitBranch   string `json:"git_branch"`
	Title       string `json:"title"`
	Environment struct {
		WorkingDirectory string `json:"working_directory"`
	} `json:"environment"`
	Config struct {
		ActiveModel string `json:"active_model"`
	} `json:"config"`
	Stats struct {
		SessionPromptTokens     int `json:"session_prompt_tokens"`
		SessionCompletionTokens int `json:"session_completion_tokens"`
	} `json:"stats"`
}

// DiscoverVibeSessions finds all session directories under the
// Vibe sessions directory. Layout:
// <sessionsDir>/session_<timestamp>_<hash>/messages.jsonl
func DiscoverVibeSessions(sessionsDir string) []DiscoveredFile {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil
	}

	var files []DiscoveredFile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		msgPath := filepath.Join(sessionsDir, e.Name(), "messages.jsonl")
		if _, err := os.Stat(msgPath); err != nil {
			continue
		}
		files = append(files, DiscoveredFile{
			Path:  msgPath,
			Agent: AgentVibe,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files
}

// FindVibeSourceFile locates a Vibe session file by its raw
// session ID (without the "vibe:" prefix). The raw ID is the
// session directory name.
func FindVibeSourceFile(sessionsDir, rawID string) string {
	if sessionsDir == "" || !IsValidSessionID(rawID) {
		return ""
	}
	candidate := filepath.Join(sessionsDir, rawID, "messages.jsonl")
	if abs, err := filepath.Abs(candidate); err != nil ||
		!strings.HasPrefix(abs, filepath.Clean(sessionsDir)) {
		return ""
	}
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

// loadVibeMeta reads the companion meta.json file for a session.
func loadVibeMeta(messagesPath string) *vibeMeta {
	metaPath := filepath.Join(filepath.Dir(messagesPath), "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil
	}
	var m vibeMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

// ParseVibeSession parses a Mistral Vibe CLI session from its
// messages.jsonl file. Returns (nil, nil, nil) if the file is
// missing or contains no user/assistant messages.
//
// Vibe message format (OpenAI-compatible):
//   - User: {"role":"user", "content":"...", "message_id":"..."}
//   - Assistant: {"role":"assistant", "content":"...", "tool_calls":[...], "message_id":"..."}
//   - Tool: {"role":"tool", "content":"...", "name":"...", "tool_call_id":"..."}
func ParseVibeSession(
	path, machine string,
) (*ParsedSession, []ParsedMessage, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	lr := newLineReader(f, maxLineSize)
	var messages []ParsedMessage
	var firstMessage string
	ordinal := 0

	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		if !gjson.Valid(line) {
			continue
		}

		role := gjson.Get(line, "role").Str

		switch role {
		case "user":
			content := strings.TrimSpace(gjson.Get(line, "content").Str)
			if content == "" {
				continue
			}
			if firstMessage == "" {
				firstMessage = truncate(
					strings.ReplaceAll(content, "\n", " "), 300,
				)
			}
			messages = append(messages, ParsedMessage{
				Ordinal:       ordinal,
				Role:          RoleUser,
				Content:       content,
				ContentLength: len(content),
			})
			ordinal++

		case "assistant":
			content := strings.TrimSpace(gjson.Get(line, "content").Str)

			var toolCalls []ParsedToolCall
			tcArray := gjson.Get(line, "tool_calls")
			if tcArray.IsArray() {
				tcArray.ForEach(func(_, tc gjson.Result) bool {
					name := tc.Get("function.name").Str
					if name == "" {
						return true
					}
					toolCalls = append(toolCalls, ParsedToolCall{
						ToolUseID: tc.Get("id").Str,
						ToolName:  name,
						Category:  NormalizeToolCategory(name),
						InputJSON: tc.Get("function.arguments").Str,
					})
					return true
				})
			}
			hasToolUse := len(toolCalls) > 0

			if content == "" && !hasToolUse {
				continue
			}

			messages = append(messages, ParsedMessage{
				Ordinal:       ordinal,
				Role:          RoleAssistant,
				Content:       content,
				ContentLength: len(content),
				HasToolUse:    hasToolUse,
				ToolCalls:     toolCalls,
			})
			ordinal++

		case "tool":
			toolCallID := gjson.Get(line, "tool_call_id").Str
			if toolCallID == "" {
				continue
			}
			content := gjson.Get(line, "content").Str
			quoted, _ := json.Marshal(content)

			messages = append(messages, ParsedMessage{
				Ordinal:       ordinal,
				Role:          RoleUser,
				Content:       "",
				ContentLength: len(content),
				ToolResults: []ParsedToolResult{{
					ToolUseID:     toolCallID,
					ContentRaw:    string(quoted),
					ContentLength: len(content),
				}},
			})
			ordinal++
		}
	}

	if err := lr.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading vibe %s: %w", path, err)
	}

	hasContent := false
	for _, m := range messages {
		if m.Content != "" {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return nil, nil, nil
	}

	meta := loadVibeMeta(path)

	// Session ID from directory name: session_20260303_142051_7972424f
	sessionID := filepath.Base(filepath.Dir(path))

	var project, cwd, gitBranch, model string
	var startedAt, endedAt = info.ModTime(), info.ModTime()

	if meta != nil {
		if meta.SessionID != "" {
			sessionID = meta.SessionID
		}
		cwd = meta.Environment.WorkingDirectory
		if cwd != "" {
			project = ExtractProjectFromCwd(cwd)
		}
		gitBranch = meta.GitBranch
		if meta.Title != "" && firstMessage == "" {
			firstMessage = meta.Title
		}
		if t := parseTimestamp(meta.StartTime); !t.IsZero() {
			startedAt = t
		}
		if t := parseTimestamp(meta.EndTime); !t.IsZero() {
			endedAt = t
		}
		model = meta.Config.ActiveModel
	}

	if project == "" {
		project = "unknown"
	}

	fullID := "vibe:" + sessionID

	userCount := 0
	for _, m := range messages {
		if m.Role == RoleUser && m.Content != "" {
			userCount++
		}
	}

	// Set model and timestamp on all assistant messages, and attach
	// session-level token totals to the last assistant message.
	// Vibe messages.jsonl has no per-message timestamps, so we use
	// the session start time for all messages so that the usage
	// query's COALESCE(m.timestamp, s.started_at) resolves correctly
	// (m.timestamp is stored as '' not NULL, so COALESCE won't
	// fall through without an explicit value).
	lastAssistant := -1
	for i := range messages {
		messages[i].Timestamp = startedAt
		if messages[i].Role == RoleAssistant {
			if model != "" {
				messages[i].Model = model
			}
			lastAssistant = i
		}
	}
	if lastAssistant >= 0 && meta != nil {
		inputTokens := meta.Stats.SessionPromptTokens
		outputTokens := meta.Stats.SessionCompletionTokens
		if inputTokens > 0 || outputTokens > 0 {
			normalized := map[string]int{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
			}
			j, _ := json.Marshal(normalized)
			messages[lastAssistant].TokenUsage = j
			messages[lastAssistant].ContextTokens = inputTokens
			messages[lastAssistant].HasContextTokens = true
			messages[lastAssistant].OutputTokens = outputTokens
			messages[lastAssistant].HasOutputTokens = true
		}
	}

	sess := &ParsedSession{
		ID:               fullID,
		Project:          project,
		Machine:          machine,
		Agent:            AgentVibe,
		Cwd:              cwd,
		GitBranch:        gitBranch,
		FirstMessage:     firstMessage,
		StartedAt:        startedAt,
		EndedAt:          endedAt,
		MessageCount:     len(messages),
		UserMessageCount: userCount,
		File: FileInfo{
			Path:  path,
			Size:  info.Size(),
			Mtime: info.ModTime().UnixNano(),
		},
	}

	accumulateMessageTokenUsage(sess, messages)

	return sess, messages, nil
}

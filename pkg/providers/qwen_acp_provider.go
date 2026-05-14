package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/cli"
)

// qwenACPEnvelope is a single NDJSON line exchanged over the
// agentclientprotocol.com (ACP) stdio transport.
//
// Agent responses have id + result/error.
// Agent-initiated notifications have method + params (no id).
// Agent-initiated requests have id + method + params.
type qwenACPEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// QwenACPProvider drives qwen-code --acp as a subprocess and speaks the
// Agent Communication Protocol (ACP) over its stdin/stdout using NDJSON
// framed JSON-RPC.
//
// Protocol handshake per call:
//  1. initialize   → protocolVersion + clientCapabilities
//  2. session/new  → cwd  (agent returns sessionId)
//  3. session/prompt → prompt text (agent streams session/update notifications,
//     then responds with stopReason)
type QwenACPProvider struct {
	// command is the executable to launch (default: "qwen").
	// Override via ModelConfig.APIBase for non-standard installations.
	// APIBase may contain space-separated args, e.g. "npx @qwen-code/qwen-code@latest".
	command   string
	// extraArgs are prepended before the ACP flag, e.g. ["@qwen-code/qwen-code@latest"] for npx.
	extraArgs  []string
	workspace string
	// acpFlagOnce ensures acpFlag() is only detected once, even under concurrent calls.
	acpFlagOnce  sync.Once
	cachedACPFlag string
}

// NewQwenACPProvider creates a QwenACPProvider.
// commandLine may be a single binary ("qwen") or space-separated command+args
// ("npx @qwen-code/qwen-code@latest"). Defaults to "qwen" when empty.
func NewQwenACPProvider(commandLine, workspace string) *QwenACPProvider {
	if commandLine == "" {
		commandLine = "qwen"
	}
	parts := strings.Fields(commandLine)
	return &QwenACPProvider{command: parts[0], extraArgs: parts[1:], workspace: workspace}
}

// GetDefaultModel returns the canonical model identifier for config matching.
func (p *QwenACPProvider) GetDefaultModel() string { return "qwen-code" }

// acpFlag returns the correct ACP startup flag for the installed qwen binary.
// Versions ≥ 1.x use --acp; v0.0.x uses --experimental-acp.
// The result is detected once and cached on the provider instance.
func (p *QwenACPProvider) acpFlag() string {
	p.acpFlagOnce.Do(func() {
		helpArgs := append(append([]string{}, p.extraArgs...), "--help")
		out, err := exec.Command(p.command, helpArgs...).CombinedOutput()
		p.cachedACPFlag = "--experimental-acp" // default for old versions
		if err == nil {
			// Check each line for a standalone --acp option (not --experimental-acp).
			// Help lines look like "      --acp    Starts the agent in ACP mode".
			for _, line := range strings.Split(string(out), "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "--acp" || strings.HasPrefix(trimmed, "--acp ") || strings.HasPrefix(trimmed, "--acp\t") {
					p.cachedACPFlag = "--acp"
					break
				}
			}
		}
	})
	return p.cachedACPFlag
}

// Chat implements LLMProvider by spawning the qwen --acp subprocess for every
// request.  Each call is a fresh ACP session; the subprocess exits once we
// close stdin.
func (p *QwenACPProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	return p.ChatWithStream(ctx, messages, tools, model, options, nil)
}

// ChatWithStream is like Chat but calls onChunk (if non-nil) with each text chunk
// as it arrives from the agent.  This enables the caller to stream responses
// (e.g. via SSE) without waiting for the full reply.
func (p *QwenACPProvider) ChatWithStream(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
	onChunk func(string),
) (*LLMResponse, error) {
	promptText := p.buildPrompt(messages, tools)

	// v0.0.x uses --experimental-acp; newer versions added --acp (still accept both).
	// We detect which flag is supported by checking the binary once.
	acpFlag := p.acpFlag()
	// --approval-mode=yolo prevents qwen from blocking on interactive permission prompts.
	// extraArgs (e.g. from "npx @qwen-code/qwen-code@latest") are inserted before the ACP flag.
	cmdArgs := append(append([]string{}, p.extraArgs...), acpFlag, "--approval-mode=yolo")
	cmd := exec.CommandContext(ctx, p.command, cmdArgs...)
	// Put qwen in its own process group so we can kill it and all its children.
	qwenSetProcessGroup(cmd)
	// Do not set cmd.Dir — qwen uses the session/new cwd param instead.
	// Setting cmd.Dir to a non-git workspace causes qwen to stall on project init.

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("qwen acp: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("qwen acp: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("qwen acp: failed to start %q: %w", p.command, err)
	}
	defer func() {
		stdinPipe.Close()
		// qwen may not exit on its own after stdin is closed; give it a moment
		// then force-kill to avoid blocking the caller indefinitely.
		done := make(chan struct{})
		go func() {
			cmd.Wait() //nolint:errcheck
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			// Kill the entire process group to catch any children qwen spawned.
			qwenKillProcessGroup(cmd)
			<-done
		}
	}()

	content, usage, err := p.runSession(ctx, stdinPipe, stdoutPipe, promptText, onChunk)
	if err != nil {
		return nil, err
	}

	toolCalls := cliprovider.ExtractToolCallsFromText(content)
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		content = cliprovider.StripToolCallsFromText(content)
	}

	return &LLMResponse{
		Content:      strings.TrimSpace(content),
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage:        usage,
	}, nil
}

// runSession drives the full ACP JSON-RPC handshake over the subprocess pipes.
// onChunk, if non-nil, is called with each text chunk as it arrives from the agent.
func (p *QwenACPProvider) runSession(
	ctx context.Context, w io.Writer, r io.Reader, prompt string, onChunk func(string),
) (string, *UsageInfo, error) {
	enc := json.NewEncoder(w)
	var idSeq int64
	nextID := func() int64 {
		idSeq++
		return idSeq
	}
	send := func(v any) error { return enc.Encode(v) }

	// Stream NDJSON lines from stdout in a background goroutine so that we can
	// select on context cancellation while waiting for responses.
	type lineResult struct {
		env *qwenACPEnvelope
		err error
	}
	lines := make(chan lineResult, 64)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 4<<20), 4<<20)
		for sc.Scan() {
			text := strings.TrimSpace(sc.Text())
			if text == "" {
				continue
			}
			var env qwenACPEnvelope
			if jsonErr := json.Unmarshal([]byte(text), &env); jsonErr != nil {
				continue // skip malformed lines
			}
			lines <- lineResult{env: &env}
		}
		if scErr := sc.Err(); scErr != nil && scErr != io.EOF {
			lines <- lineResult{err: scErr}
		}
		close(lines)
	}()

	recv := func() (*qwenACPEnvelope, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res, ok := <-lines:
			if !ok {
				return nil, io.EOF
			}
			return res.env, res.err
		}
	}

	// sendReq sends a JSON-RPC request and blocks until it gets the matching
	// response, handling any intervening permission requests from the agent.
	sendReq := func(id int64, method string, params any) (json.RawMessage, error) {
		if err := send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, fmt.Errorf("%s send: %w", method, err)
		}
		idJSON, _ := json.Marshal(id)
		idStr := string(idJSON)
		for {
			env, err := recv()
			if err != nil {
				return nil, fmt.Errorf("%s recv: %w", method, err)
			}
			if (env.Result != nil || env.Error != nil) && string(env.ID) == idStr {
				if env.Error != nil {
					return nil, fmt.Errorf("%s: ACP error %d: %s", method, env.Error.Code, env.Error.Message)
				}
				return env.Result, nil
			}
			// Handle permission requests that arrive while waiting.
			if env.Method == "session/request_permission" && len(env.ID) > 0 {
				handlePermission(send, env)
			}
		}
	}

	cwd := p.workspace
	if cwd == "" {
		cwd = "."
	}

	// ── Step 1: initialize ────────────────────────────────────────────────────

	if _, err := sendReq(nextID(), "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	}); err != nil {
		return "", nil, fmt.Errorf("qwen acp: %w", err)
	}

	// ── Step 2: session/new ───────────────────────────────────────────────────

	sessData, err := sendReq(nextID(), "session/new", map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	if err != nil {
		return "", nil, fmt.Errorf("qwen acp: %w", err)
	}
	var sessResp struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(sessData, &sessResp); err != nil || sessResp.SessionID == "" {
		return "", nil, fmt.Errorf("qwen acp: session/new: missing sessionId in %s", sessData)
	}

	// ── Step 3: session/prompt ────────────────────────────────────────────────
	promptID := nextID()
	promptIDJSON, _ := json.Marshal(promptID)
	promptIDStr := string(promptIDJSON)

	if err := send(map[string]any{
		"id":     promptID,
		"method": "session/prompt",
		"params": map[string]any{
			"sessionId": sessResp.SessionID,
			"prompt":    []any{map[string]any{"type": "text", "text": prompt}},
		},
	}); err != nil {
		return "", nil, fmt.Errorf("qwen acp: session/prompt send: %w", err)
	}

	// Collect streaming output until session/prompt response arrives.
	var parts []string
	var usage *UsageInfo

	for {
		env, err := recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return strings.Join(parts, ""), usage, fmt.Errorf("qwen acp: recv: %w", err)
		}

		// session/prompt response → done
		if (env.Result != nil || env.Error != nil) && string(env.ID) == promptIDStr {
			if env.Error != nil {
				return strings.Join(parts, ""), usage, fmt.Errorf(
					"qwen acp: prompt: ACP error %d: %s", env.Error.Code, env.Error.Message)
			}
			break
		}

		switch env.Method {
		case "session/update":
			collectUpdate(env.Params, &parts, &usage, onChunk)
		case "session/request_permission":
			if len(env.ID) > 0 {
				handlePermission(send, env)
			}
		}
	}

	return strings.Join(parts, ""), usage, nil
}

// collectUpdate parses a session/update notification and appends any
// agent_message_chunk text to parts, updating usage on usage_update.
// onChunk is called with each text chunk if non-nil.
func collectUpdate(raw json.RawMessage, parts *[]string, usage **UsageInfo, onChunk func(string)) {
	var notif struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			// usage_update fields
			Used int `json:"used"`
			Size int `json:"size"`
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &notif); err != nil {
		return
	}
	switch notif.Update.SessionUpdate {
	case "agent_message_chunk":
		if notif.Update.Content.Type == "text" {
			*parts = append(*parts, notif.Update.Content.Text)
			if onChunk != nil {
				onChunk(notif.Update.Content.Text)
			}
		}
	case "usage_update":
		*usage = &UsageInfo{
			PromptTokens: notif.Update.Used,
			TotalTokens:  notif.Update.Used,
		}
	}
}

// handlePermission responds to a session/request_permission agent request.
// It selects "allow_always" if available, otherwise cancels.
func handlePermission(send func(any) error, env *qwenACPEnvelope) {
	var permReq struct {
		Options []struct {
			Kind     string `json:"kind"`
			OptionID string `json:"optionId"`
		} `json:"options"`
	}
	optionID := ""
	if json.Unmarshal(env.Params, &permReq) == nil {
		for _, opt := range permReq.Options {
			if opt.Kind == "allow_always" || optionID == "" {
				optionID = opt.OptionID
			}
			if opt.Kind == "allow_always" {
				break
			}
		}
	}
	if optionID != "" {
		_ = send(map[string]any{
			"id": json.RawMessage(env.ID),
			"result": map[string]any{
				"outcome": map[string]any{
					"outcome":  "selected",
					"optionId": optionID,
				},
			},
		})
	} else {
		_ = send(map[string]any{
			"id":     json.RawMessage(env.ID),
			"result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}},
		})
	}
}

// buildPrompt converts the message slice into a single text block for qwen-code.
// Tool definitions are intentionally omitted — qwen-code has its own native tools
// and adding picoclaw's tool JSON as text causes it to run unwanted agentic loops.
func (p *QwenACPProvider) buildPrompt(messages []Message, _ []ToolDefinition) string {
	var sb strings.Builder
	var userParts []string
	var systemParts []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)
		case "user":
			userParts = append(userParts, msg.Content)
		case "assistant":
			userParts = append(userParts, "Assistant: "+msg.Content)
		case "tool":
			userParts = append(userParts, fmt.Sprintf("[Tool Result for %s]: %s", msg.ToolCallID, msg.Content))
		}
	}

	if len(systemParts) > 0 {
		fmt.Fprintf(&sb, "## Instructions\n\n%s\n\n", strings.Join(systemParts, "\n\n"))
	}

	// Single user message with no system: send bare
	if len(userParts) == 1 && len(systemParts) == 0 {
		return strings.TrimSpace(userParts[0])
	}

	sb.WriteString(strings.Join(userParts, "\n"))
	return strings.TrimSpace(sb.String())
}

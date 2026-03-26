// Package bridge exposes a lightweight HTTP API in front of the qwen ACP
// provider.  It is purely additive: disabled by default and independent of all
// existing channels and agent-loop internals.
//
// Endpoints:
//
//	POST /chat        – synchronous, returns {"reply":"...","tokens":N}
//	POST /chat/stream – SSE stream; each event is a JSON text chunk
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"crypto/rand"
	"encoding/hex"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// ProviderFromModelList finds the first qwen-code entry in cfg.ModelList and
// returns a ready-to-use QwenACPProvider.  Returns nil if no suitable model is
// found.
func ProviderFromModelList(cfg *config.Config) *providers.QwenACPProvider {
	for _, m := range cfg.ModelList {
		switch m.ModelName {
		case "qwen-code", "qwen-cli", "qwencli", "qwen-acp":
			ws := m.Workspace
			if ws == "" {
				ws = "."
			}
			return providers.NewQwenACPProvider(m.APIBase, ws)
		}
	}
	return nil
}

// Bridge wraps a QwenACPProvider behind a minimal HTTP server.
type Bridge struct {
	cfg      config.BridgeConfig
	provider *providers.QwenACPProvider
	srv      *http.Server
}

// New creates a Bridge using the given config and provider.
// The provider should already be initialised with the correct command and
// workspace; the bridge does not own the provider's lifecycle.
func New(cfg config.BridgeConfig, provider *providers.QwenACPProvider) *Bridge {
	return &Bridge{cfg: cfg, provider: provider}
}

// Start begins listening.  It returns immediately; the server runs in a
// goroutine.  Call Stop to shut down cleanly.
func (b *Bridge) Start(ctx context.Context) error {
	listen := b.cfg.Listen
	if listen == "" {
		listen = ":9090"
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("bridge: listen %s: %w", listen, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/chat", b.handleSync)
	mux.HandleFunc("/chat/stream", b.handleStream)
	mux.HandleFunc("/v1/chat/completions", b.handleOpenAI)

	b.srv = &http.Server{
		Handler:      b.withAuth(mux),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 0, // disabled for SSE
	}

	go func() {
		if err := b.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.ErrorCF("bridge", "server error", map[string]any{"error": err.Error()})
		}
	}()

	fmt.Printf("✓ Bridge started on %s\n", ln.Addr())
	return nil
}

// Stop gracefully shuts the HTTP server down.
func (b *Bridge) Stop(ctx context.Context) {
	if b.srv != nil {
		_ = b.srv.Shutdown(ctx)
	}
}

// ── auth middleware ──────────────────────────────────────────────────────────

func (b *Bridge) withAuth(next http.Handler) http.Handler {
	if b.cfg.APIKey == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || token != b.cfg.APIKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── request / response types ─────────────────────────────────────────────────

type chatRequest struct {
	Message string `json:"message"`
}

type chatResponse struct {
	Reply  string `json:"reply"`
	Tokens int    `json:"tokens,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleSync handles POST /chat — blocks until qwen is done, then returns the
// full reply as JSON.
func (b *Bridge) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"message field required"})
		return
	}

	resp, err := b.provider.ChatWithStream(r.Context(), []providers.Message{
		{Role: "user", Content: req.Message},
	}, nil, "qwen-code", nil, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}

	tokens := 0
	if resp.Usage != nil {
		tokens = resp.Usage.TotalTokens
	}
	writeJSON(w, http.StatusOK, chatResponse{Reply: resp.Content, Tokens: tokens})
}

// handleStream handles POST /chat/stream — sends each text chunk as an SSE
// event as it arrives from qwen, then closes the connection.
func (b *Bridge) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{"message field required"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if behind a proxy

	onChunk := func(chunk string) {
		data, _ := json.Marshal(map[string]string{"chunk": chunk})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	resp, err := b.provider.ChatWithStream(r.Context(), []providers.Message{
		{Role: "user", Content: req.Message},
	}, nil, "qwen-code", nil, onChunk)
	if err != nil {
		data, _ := json.Marshal(errorResponse{err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}

	// Send a final "done" event with the full reply and token count.
	tokens := 0
	if resp != nil && resp.Usage != nil {
		tokens = resp.Usage.TotalTokens
	}
	doneData, _ := json.Marshal(map[string]any{"done": true, "tokens": tokens})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", doneData)
	flusher.Flush()
}

// ── OpenAI-compatible endpoint ────────────────────────────────────────────────

// openAIRequest mirrors the subset of the OpenAI chat completions request we need.
type openAIRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

// handleOpenAI handles POST /v1/chat/completions in OpenAI wire format.
// Supports both non-streaming (stream=false) and streaming (stream=true) modes
// so that any OpenAI-compatible client can talk to the bridge.
func (b *Bridge) handleOpenAI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req openAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"invalid request body"})
		return
	}
	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{"messages is required"})
		return
	}

	// Convert OpenAI messages to provider messages.
	msgs := make([]providers.Message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = providers.Message{Role: m.Role, Content: m.Content}
	}

	chatID := newChatID()
	model := req.Model
	if model == "" {
		model = "qwen-code"
	}

	if req.Stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		onChunk := func(chunk string) {
			ev, _ := json.Marshal(map[string]any{
				"id":      chatID,
				"object":  "chat.completion.chunk",
				"model":   model,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": chunk}, "finish_reason": nil}},
			})
			fmt.Fprintf(w, "data: %s\n\n", ev)
			flusher.Flush()
		}

		resp, err := b.provider.ChatWithStream(r.Context(), msgs, nil, model, nil, onChunk)
		if err != nil {
			ev, _ := json.Marshal(errorResponse{err.Error()})
			fmt.Fprintf(w, "data: %s\n\n", ev)
			flusher.Flush()
			return
		}

		// Final chunk with finish_reason=stop.
		tokens := 0
		if resp != nil && resp.Usage != nil {
			tokens = resp.Usage.TotalTokens
		}
		finalEv, _ := json.Marshal(map[string]any{
			"id":      chatID,
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}},
			"usage":   map[string]int{"total_tokens": tokens},
		})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finalEv)
		flusher.Flush()
		return
	}

	// Non-streaming.
	resp, err := b.provider.ChatWithStream(r.Context(), msgs, nil, model, nil, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{err.Error()})
		return
	}
	tokens := 0
	if resp != nil && resp.Usage != nil {
		tokens = resp.Usage.TotalTokens
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      chatID,
		"object":  "chat.completion",
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": resp.Content}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 0, "completion_tokens": tokens, "total_tokens": tokens},
	})
}

// newChatID generates a short random ID for OpenAI-format responses.
func newChatID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

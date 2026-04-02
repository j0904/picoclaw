package acp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	defaultRunTTL       = 3600 * time.Second
	defaultListen       = ":8765"
	defaultSyncTimeout  = 5 * time.Minute
	senderID            = "acp-client"
)

// runEntry holds the in-flight state for a single ACP run.
type runEntry struct {
	response  RunResponse
	done      chan struct{} // closed when the run reaches a terminal state
	sseClients []chan string // each registered SSE client gets a copy of events
	mu        sync.Mutex
	cancel    context.CancelFunc
}

// appendOutput appends an assistant message, transitions status to completed, and
// notifies all waiters.
func (e *runEntry) appendOutput(content string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.response.Status != RunStatusInProgress {
		return // already terminal
	}

	msg := Message{Role: "assistant", Content: content}
	e.response.Output = append(e.response.Output, msg)
	e.response.Status = RunStatusCompleted
	e.response.UpdatedAt = time.Now()

	// Fan-out to SSE clients
	event := sseData("message", content)
	for _, ch := range e.sseClients {
		select {
		case ch <- event:
		default:
		}
	}
	doneEvent := sseData("done", "")
	for _, ch := range e.sseClients {
		select {
		case ch <- doneEvent:
		default:
		}
	}

	select {
	case <-e.done:
	default:
		close(e.done)
	}
}

// failRun marks a run as failed and notifies waiters.
func (e *runEntry) failRun(errMsg string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.response.Status != RunStatusInProgress && e.response.Status != RunStatusCreated {
		return
	}

	e.response.Status = RunStatusFailed
	e.response.Error = errMsg
	e.response.UpdatedAt = time.Now()

	event := sseData("error", errMsg)
	for _, ch := range e.sseClients {
		select {
		case ch <- event:
		default:
		}
	}

	select {
	case <-e.done:
	default:
		close(e.done)
	}
}

// cancelRun marks a run as cancelled and notifies waiters.
func (e *runEntry) cancelRun() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.response.Status != RunStatusInProgress && e.response.Status != RunStatusCreated {
		return
	}

	e.response.Status = RunStatusCancelled
	e.response.UpdatedAt = time.Now()

	event := sseData("error", "cancelled")
	for _, ch := range e.sseClients {
		select {
		case ch <- event:
		default:
		}
	}

	select {
	case <-e.done:
	default:
		close(e.done)
	}

	if e.cancel != nil {
		e.cancel()
	}
}

// sseData formats an SSE event line.
func sseData(event, data string) string {
	if data == "" {
		return "event: " + event + "\ndata: \n\n"
	}
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// ACPChannel implements an HTTP server that exposes the Agent Communication
// Protocol (ACP) REST API. It integrates with picoclaw via the standard
// channels.Channel interface so that inbound /runs requests are dispatched
// through the normal agent loop.
type ACPChannel struct {
	*channels.BaseChannel
	cfg        config.ACPConfig
	server     *http.Server
	runs       sync.Map // runID → *runEntry
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewACPChannel constructs an ACPChannel from config.
func NewACPChannel(cfg config.ACPConfig, msgBus *bus.MessageBus) (*ACPChannel, error) {
	base := channels.NewBaseChannel("acp", cfg, msgBus, cfg.AllowFrom)

	ch := &ACPChannel{
		BaseChannel: base,
		cfg:         cfg,
	}

	listen := cfg.Listen
	if listen == "" {
		listen = defaultListen
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/runs", ch.handleRuns)
	mux.HandleFunc("/runs/", ch.handleRunByID)
	mux.HandleFunc("/health", ch.handleHealth)

	ch.server = &http.Server{
		Addr:    listen,
		Handler: ch.corsMiddleware(mux),
	}

	return ch, nil
}

// Start implements channels.Channel.
func (c *ACPChannel) Start(ctx context.Context) error {
	logger.InfoCF("acp", "Starting ACP channel", map[string]any{"addr": c.server.Addr})
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)

	go func() {
		if err := c.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.ErrorCF("acp", "ACP server error", map[string]any{"error": err.Error()})
		}
	}()

	go c.janitor()

	logger.InfoCF("acp", "ACP channel started", map[string]any{"addr": c.server.Addr})
	return nil
}

// Stop implements channels.Channel.
func (c *ACPChannel) Stop(ctx context.Context) error {
	logger.InfoC("acp", "Stopping ACP channel")
	c.SetRunning(false)

	if c.cancel != nil {
		c.cancel()
	}
	if err := c.server.Shutdown(ctx); err != nil {
		logger.WarnCF("acp", "ACP server shutdown error", map[string]any{"error": err.Error()})
	}

	logger.InfoC("acp", "ACP channel stopped")
	return nil
}

// Send implements channels.Channel — delivers an outbound message back to the waiting run.
func (c *ACPChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	runID := strings.TrimPrefix(msg.ChatID, "acp:")
	v, ok := c.runs.Load(runID)
	if !ok {
		return nil // run may have been cancelled or expired
	}

	entry := v.(*runEntry)
	entry.appendOutput(msg.Content)
	return nil
}

// corsMiddleware sets CORS headers for all responses.
func (c *ACPChannel) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if c.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// originAllowed checks whether the given origin is permitted.
func (c *ACPChannel) originAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	if len(c.cfg.AllowOrigins) == 0 {
		return true // no restriction configured — allow all
	}
	for _, allowed := range c.cfg.AllowOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

// authenticate verifies the Bearer token when api_key is configured.
func (c *ACPChannel) authenticate(r *http.Request) bool {
	if c.cfg.APIKey == "" {
		return true // no auth configured
	}
	auth := r.Header.Get("Authorization")
	after, ok := strings.CutPrefix(auth, "Bearer ")
	return ok && after == c.cfg.APIKey
}

// handleHealth handles GET /health.
func (c *ACPChannel) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// handleRuns handles POST /runs.
func (c *ACPChannel) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req RunCreateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Input) == 0 {
		http.Error(w, "input is required", http.StatusBadRequest)
		return
	}

	// Collect the last user message as the prompt.
	var userContent string
	for i := len(req.Input) - 1; i >= 0; i-- {
		if req.Input[i].Role == "user" {
			userContent = req.Input[i].Content
			break
		}
	}
	if strings.TrimSpace(userContent) == "" {
		http.Error(w, "no user message found in input", http.StatusBadRequest)
		return
	}

	runID := uuid.New().String()
	agentID := req.AgentID

	runCtx, runCancel := context.WithCancel(c.ctx)

	entry := &runEntry{
		response: RunResponse{
			RunID:     runID,
			AgentID:   agentID,
			Status:    RunStatusCreated,
			Input:     req.Input,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		done:   make(chan struct{}),
		cancel: runCancel,
	}
	c.runs.Store(runID, entry)

	// Transition to in-progress before publishing.
	entry.mu.Lock()
	entry.response.Status = RunStatusInProgress
	entry.response.UpdatedAt = time.Now()
	entry.mu.Unlock()

	chatID := "acp:" + runID
	peer := bus.Peer{Kind: "direct", ID: chatID}
	sender := bus.SenderInfo{
		Platform:    "acp",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("acp", senderID),
	}

	metadata := map[string]string{
		"platform": "acp",
		"run_id":   runID,
	}
	if agentID != "" {
		metadata["agent_id"] = agentID
	}

	c.HandleMessage(runCtx, peer, runID, senderID, chatID, userContent, nil, metadata, sender)

	// Determine whether to stream.
	wantsSSE := req.Stream || r.Header.Get("Accept") == "text/event-stream"

	if wantsSSE {
		c.serveSSE(w, r, entry, runID)
		return
	}

	// Synchronous: wait for the run to finish.
	ttl := time.Duration(c.cfg.RunTTL) * time.Second
	if ttl <= 0 {
		ttl = defaultSyncTimeout
	}
	timer := time.NewTimer(ttl)
	defer timer.Stop()

	select {
	case <-entry.done:
	case <-timer.C:
		entry.failRun("timeout waiting for agent response")
	case <-runCtx.Done():
	}

	entry.mu.Lock()
	resp := entry.response
	entry.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	statusCode := http.StatusOK
	if resp.Status == RunStatusFailed {
		statusCode = http.StatusInternalServerError
	}
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}

// serveSSE streams run events to the client using Server-Sent Events.
func (c *ACPChannel) serveSSE(w http.ResponseWriter, r *http.Request, entry *runEntry, runID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	eventCh := make(chan string, 16)

	entry.mu.Lock()
	// If already done, send what we have immediately.
	alreadyDone := entry.response.Status != RunStatusCreated &&
		entry.response.Status != RunStatusInProgress
	if !alreadyDone {
		entry.sseClients = append(entry.sseClients, eventCh)
	}
	entry.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if alreadyDone {
		// Already completed before the SSE client connected.
		entry.mu.Lock()
		resp := entry.response
		entry.mu.Unlock()
		if len(resp.Output) > 0 {
			w.Write([]byte(sseData("message", resp.Output[len(resp.Output)-1].Content)))
		}
		w.Write([]byte(sseData("done", "")))
		flusher.Flush()
		return
	}

	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				return
			}
			w.Write([]byte(event))
			flusher.Flush()
			if strings.HasPrefix(event, "event: done") || strings.HasPrefix(event, "event: error") {
				return
			}
		case <-r.Context().Done():
			return
		case <-entry.done:
			// Drain remaining buffered events.
			for {
				select {
				case event := <-eventCh:
					w.Write([]byte(event))
					flusher.Flush()
				default:
					return
				}
			}
		}
	}
}

// handleRunByID handles GET /runs/{id} and DELETE /runs/{id}.
func (c *ACPChannel) handleRunByID(w http.ResponseWriter, r *http.Request) {
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	runID := strings.TrimPrefix(r.URL.Path, "/runs/")
	if runID == "" {
		http.Error(w, "run_id is required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		c.handleGetRun(w, r, runID)
	case http.MethodDelete:
		c.handleCancelRun(w, r, runID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (c *ACPChannel) handleGetRun(w http.ResponseWriter, r *http.Request, runID string) {
	v, ok := c.runs.Load(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	entry := v.(*runEntry)
	entry.mu.Lock()
	resp := entry.response
	entry.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (c *ACPChannel) handleCancelRun(w http.ResponseWriter, r *http.Request, runID string) {
	v, ok := c.runs.Load(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	entry := v.(*runEntry)
	entry.cancelRun()

	w.WriteHeader(http.StatusNoContent)
}

// janitor periodically removes expired runs from the store.
func (c *ACPChannel) janitor() {
	ttl := time.Duration(c.cfg.RunTTL) * time.Second
	if ttl <= 0 {
		ttl = defaultRunTTL
	}

	ticker := time.NewTicker(ttl / 2)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-ttl)
			c.runs.Range(func(key, value any) bool {
				entry := value.(*runEntry)
				entry.mu.Lock()
				updatedAt := entry.response.UpdatedAt
				isTerminal := entry.response.Status == RunStatusCompleted ||
					entry.response.Status == RunStatusFailed ||
					entry.response.Status == RunStatusCancelled
				entry.mu.Unlock()

				if isTerminal && updatedAt.Before(cutoff) {
					c.runs.Delete(key)
				}
				return true
			})
		}
	}
}

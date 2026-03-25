package acp

import "time"

// RunStatus represents the lifecycle state of an ACP run.
type RunStatus string

const (
	RunStatusCreated    RunStatus = "created"
	RunStatusInProgress RunStatus = "in_progress"
	RunStatusCompleted  RunStatus = "completed"
	RunStatusFailed     RunStatus = "failed"
	RunStatusCancelled  RunStatus = "cancelled"
)

// Message is a single turn in the conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// RunCreateRequest is the body of POST /runs.
type RunCreateRequest struct {
	AgentID string    `json:"agent_id,omitempty"`
	Input   []Message `json:"input"`
	// Stream, if true (or if Accept: text/event-stream is set), triggers SSE streaming.
	Stream bool `json:"stream,omitempty"`
}

// RunResponse is returned by GET /runs/{id} and by POST /runs when not streaming.
type RunResponse struct {
	RunID     string    `json:"run_id"`
	AgentID   string    `json:"agent_id"`
	Status    RunStatus `json:"status"`
	Input     []Message `json:"input"`
	Output    []Message `json:"output,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

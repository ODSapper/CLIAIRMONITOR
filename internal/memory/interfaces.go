// Package memory provides the dual-database memory system for CLIAIRMONITOR.
// It separates operational state (agents, tasks) from learning memory (RAG, embeddings).
package memory

import (
	"time"
)

// OperationalDB handles fast, focused operational state.
// This DB is small and optimized for current session management.
type OperationalDB interface {
	// Agent lifecycle
	RegisterAgent(agent *AgentState) error
	UpdateAgentStatus(agentID string, status AgentStatus, task string) error
	GetAgent(agentID string) (*AgentState, error)
	ListAgents(filter AgentFilter) ([]*AgentState, error)
	SetShutdownFlag(agentID string, reason string) error
	RecordHeartbeat(agentID string) error
	MarkStopped(agentID string, reason string) error

	// Task queue
	CreateTask(task *Task) error
	ClaimTask(taskID, agentID string) error
	UpdateTaskProgress(taskID string, status TaskStatus, note string) error
	CompleteTask(taskID, summary string) error
	GetTask(taskID string) (*Task, error)
	ListTasks(filter TaskFilter) ([]*Task, error)

	// Session management
	CreateSession(session *Session) error
	GetSession(sessionID string) (*Session, error)
	UpdateSession(session *Session) error
	GetActiveSession(agentID string) (*Session, error)

	// Communication channels
	SendMessage(msg *Message) error
	GetMessages(agentID string, since time.Time) ([]*Message, error)
	AcknowledgeMessage(msgID string) error

	// Health and metrics
	RecordMetric(metric *Metric) error
	GetMetrics(agentID string, since time.Time) ([]*Metric, error)

	// Cleanup
	CleanupStaleAgents(threshold time.Duration) (int, error)
	Close() error
}

// LearningDB handles RAG-style memory with embeddings.
// This DB grows over time and enables learning from experience.
type LearningDB interface {
	// Episodic memory - what happened, timestamped
	RecordEpisode(episode *Episode) error
	GetEpisodes(filter EpisodeFilter) ([]*Episode, error)
	SummarizeEpisodes(sessionID string) (*EpisodeSummary, error)

	// Semantic memory - searchable knowledge with embeddings
	StoreKnowledge(knowledge *Knowledge) error
	SearchKnowledge(query string, limit int) ([]*Knowledge, error)
	SearchByEmbedding(embedding []float32, limit int) ([]*Knowledge, error)
	GetKnowledge(id string) (*Knowledge, error)
	UpdateKnowledge(knowledge *Knowledge) error

	// Procedural memory - learned workflows
	StoreProcedure(procedure *Procedure) error
	GetProcedure(id string) (*Procedure, error)
	FindProcedures(taskType string, context string) ([]*Procedure, error)
	UpdateProcedureSuccess(id string, success bool) error

	// Embedding management
	ComputeEmbedding(text string) ([]float32, error)
	SetEmbeddingProvider(provider EmbeddingProvider) error

	// Maintenance
	PruneOldEpisodes(before time.Time) (int, error)
	CompactKnowledge() error
	Close() error
}

// EmbeddingProvider generates embeddings for text.
// Can be implemented by local models or external services.
type EmbeddingProvider interface {
	Embed(text string) ([]float32, error)
	EmbedBatch(texts []string) ([][]float32, error)
	Dimensions() int
}

// ContextBuilder assembles focused context for the LLM.
// Critical for local models with limited context windows.
type ContextBuilder interface {
	// Build context within token budget
	BuildContext(request *ContextRequest) (*Context, error)

	// Estimate tokens for text
	EstimateTokens(text string) int

	// Configure budget allocation
	SetBudget(budget *ContextBudget)
}

// ================================================
// Operational Types
// ================================================

// AgentStatus represents the current state of an agent
type AgentStatus string

const (
	AgentStatusStarting    AgentStatus = "starting"
	AgentStatusConnected   AgentStatus = "connected"
	AgentStatusWorking     AgentStatus = "working"
	AgentStatusIdle        AgentStatus = "idle"
	AgentStatusBlocked     AgentStatus = "blocked"
	AgentStatusStopping    AgentStatus = "stopping"
	AgentStatusStopped     AgentStatus = "stopped"
	AgentStatusUnreachable AgentStatus = "unreachable"
)

// AgentState holds the current state of an agent
type AgentState struct {
	AgentID       string      `json:"agent_id"`
	AgentType     string      `json:"agent_type"`
	Model         string      `json:"model"`
	Status        AgentStatus `json:"status"`
	CurrentTask   string      `json:"current_task,omitempty"`
	ProjectPath   string      `json:"project_path,omitempty"`
	SessionID     string      `json:"session_id,omitempty"`
	PID           *int        `json:"pid,omitempty"`
	HeartbeatAt   *time.Time  `json:"heartbeat_at,omitempty"`
	ShutdownFlag  bool        `json:"shutdown_flag"`
	ShutdownReason string     `json:"shutdown_reason,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// AgentFilter filters agent queries
type AgentFilter struct {
	Status    AgentStatus
	AgentType string
	Active    *bool // non-stopped agents
	Limit     int
	Offset    int
}

// TaskStatus represents task state
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusClaimed    TaskStatus = "claimed"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusBlocked    TaskStatus = "blocked"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
)

// Task represents a work item
type Task struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	TaskType    string            `json:"task_type"`
	Priority    int               `json:"priority"`
	Status      TaskStatus        `json:"status"`
	AssignedTo  string            `json:"assigned_to,omitempty"`
	ProjectPath string            `json:"project_path,omitempty"`
	Progress    string            `json:"progress,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// TaskFilter filters task queries
type TaskFilter struct {
	Status     TaskStatus
	AssignedTo string
	TaskType   string
	Priority   *int
	Limit      int
	Offset     int
}

// Session represents an agent work session
type Session struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	ProjectPath string    `json:"project_path"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	TokensUsed  int64     `json:"tokens_used"`
	TasksCompleted int    `json:"tasks_completed"`
	Summary     string    `json:"summary,omitempty"`
}

// Message for inter-agent communication
type Message struct {
	ID          string    `json:"id"`
	FromAgent   string    `json:"from_agent"`
	ToAgent     string    `json:"to_agent"`
	MessageType string    `json:"message_type"` // task, signal, response, broadcast
	Content     string    `json:"content"`
	Priority    int       `json:"priority"`
	CreatedAt   time.Time `json:"created_at"`
	AckedAt     *time.Time `json:"acked_at,omitempty"`
}

// Metric for tracking agent performance
type Metric struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	MetricType string   `json:"metric_type"` // tokens, latency, errors, etc.
	Value     float64   `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

// ================================================
// Learning Types
// ================================================

// Episode represents a timestamped event in episodic memory
type Episode struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	AgentID     string    `json:"agent_id"`
	EventType   string    `json:"event_type"` // action, observation, error, decision
	Content     string    `json:"content"`
	Context     string    `json:"context,omitempty"`
	Outcome     string    `json:"outcome,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	Importance  float64   `json:"importance"` // 0-1, for retrieval weighting
}

// EpisodeFilter filters episode queries
type EpisodeFilter struct {
	SessionID string
	AgentID   string
	EventType string
	Since     time.Time
	Until     time.Time
	MinImportance float64
	Limit     int
}

// EpisodeSummary is a compressed summary of episodes
type EpisodeSummary struct {
	SessionID    string    `json:"session_id"`
	Summary      string    `json:"summary"`
	KeyEvents    []string  `json:"key_events"`
	LessonsLearned []string `json:"lessons_learned"`
	CreatedAt    time.Time `json:"created_at"`
}

// Knowledge represents a semantic memory chunk
type Knowledge struct {
	ID          string    `json:"id"`
	Category    string    `json:"category"` // code_pattern, domain, best_practice, error_solution
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	Source      string    `json:"source,omitempty"` // where this knowledge came from
	Embedding   []float32 `json:"embedding,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	UseCount    int       `json:"use_count"`
	LastUsed    *time.Time `json:"last_used,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Procedure represents a learned workflow pattern
type Procedure struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	TaskType     string    `json:"task_type"` // what kind of task this applies to
	Steps        []Step    `json:"steps"`
	Preconditions []string `json:"preconditions,omitempty"`
	SuccessCount int       `json:"success_count"`
	FailureCount int       `json:"failure_count"`
	LastUsed     *time.Time `json:"last_used,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Step is a single step in a procedure
type Step struct {
	Order       int    `json:"order"`
	Action      string `json:"action"`
	Tool        string `json:"tool,omitempty"`
	Expected    string `json:"expected,omitempty"`
	OnFailure   string `json:"on_failure,omitempty"`
}

// ================================================
// Context Building Types
// ================================================

// ContextRequest specifies what context is needed
type ContextRequest struct {
	TaskDescription string   // What the agent needs to do
	ProjectPath     string   // Current project context
	RecentActions   []string // Recent actions for continuity
	QueryHints      []string // Hints for knowledge retrieval
}

// Context is the assembled context for the LLM
type Context struct {
	SystemPrompt    string           `json:"system_prompt"`
	RelevantKnowledge []*Knowledge   `json:"relevant_knowledge,omitempty"`
	RecentEpisodes  []*Episode       `json:"recent_episodes,omitempty"`
	ApplicableProcedures []*Procedure `json:"applicable_procedures,omitempty"`
	TotalTokens     int              `json:"total_tokens"`
}

// ContextBudget controls context assembly
type ContextBudget struct {
	MaxTokens        int     `json:"max_tokens"`        // Model's context limit
	ReservedForTask  int     `json:"reserved_for_task"` // Space for task + response
	MemoryBudget     int     `json:"memory_budget"`     // What's left for RAG

	// Allocation weights (should sum to 1.0)
	EpisodicWeight   float64 `json:"episodic_weight"`   // Recent events
	SemanticWeight   float64 `json:"semantic_weight"`   // Knowledge retrieval
	ProceduralWeight float64 `json:"procedural_weight"` // Workflow patterns
}

// DefaultContextBudget for Qwen3-coder (assuming 32K context)
func DefaultContextBudget() *ContextBudget {
	return &ContextBudget{
		MaxTokens:        32000,
		ReservedForTask:  8000,  // Task description + expected response
		MemoryBudget:     24000, // Rest for memory

		EpisodicWeight:   0.3, // 30% for recent events
		SemanticWeight:   0.5, // 50% for relevant knowledge
		ProceduralWeight: 0.2, // 20% for procedures
	}
}

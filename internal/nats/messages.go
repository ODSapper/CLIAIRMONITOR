package nats

import "time"

// Subject pattern constants for NATS messaging
const (
	// SubjectAgentStatus is the pattern for agent status updates
	SubjectAgentStatus = "agent.%s.status"

	// SubjectAgentCommand is the pattern for commands sent to specific agents
	SubjectAgentCommand = "agent.%s.command"

	// SubjectAgentOutput is the pattern for agent stdout/stderr output
	SubjectAgentOutput = "agent.%s.output"

	// SubjectAllStatus subscribes to all agent status updates
	SubjectAllStatus = "agent.*.status"

	// SubjectAllOutput subscribes to all agent output
	SubjectAllOutput = "agent.*.output"

	// SubjectSergeantStatus is used for Sergeant (orchestrator) status
	SubjectSergeantStatus = "sergeant.status"

	// SubjectSergeantCommands is used for commands to Sergeant
	SubjectSergeantCommands = "sergeant.commands"

	// SubjectSystemBroadcast is used for system-wide announcements
	SubjectSystemBroadcast = "system.broadcast"

	// SubjectEscalationCreate is used when agents raise questions
	SubjectEscalationCreate = "escalation.create"

	// SubjectEscalationResponse is the pattern for responses to escalations
	SubjectEscalationResponse = "escalation.response.%s"
)

// StatusMessage represents an agent status update
type StatusMessage struct {
	AgentID     string    `json:"agent_id"`
	ConfigName  string    `json:"config_name,omitempty"`
	Status      string    `json:"status"`
	CurrentTask string    `json:"current_task"`
	Timestamp   time.Time `json:"timestamp"`
}

// CommandMessage represents a command sent to an agent
type CommandMessage struct {
	Type    string                 `json:"type"`
	Payload map[string]interface{} `json:"payload"`
}

// OutputMessage represents stdout/stderr output from an agent
type OutputMessage struct {
	AgentID   string    `json:"agent_id"`
	Stream    string    `json:"stream"` // "stdout" or "stderr"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// SergeantStatusMessage represents Sergeant orchestrator status
type SergeantStatusMessage struct {
	Status       string    `json:"status"` // idle, busy, error
	ActiveAgents int       `json:"active_agents"`
	CurrentOp    string    `json:"current_op,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
}

// SergeantCommandMessage represents commands to Sergeant
type SergeantCommandMessage struct {
	Type    string                 `json:"type"` // spawn_agent, kill_agent, pause, resume
	Payload map[string]interface{} `json:"payload"`
	From    string                 `json:"from"`
}

// EscalationCreateMessage represents an agent raising a question
type EscalationCreateMessage struct {
	ID        string                 `json:"id"`
	AgentID   string                 `json:"agent_id"`
	Question  string                 `json:"question"`
	Context   map[string]interface{} `json:"context,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// EscalationResponseMessage represents response to an escalation
type EscalationResponseMessage struct {
	ID        string    `json:"id"`
	Response  string    `json:"response"`
	From      string    `json:"from"`
	Timestamp time.Time `json:"timestamp"`
}

// SystemBroadcastMessage represents system-wide announcements
type SystemBroadcastMessage struct {
	Type      string                 `json:"type"` // shutdown, agent_crashed, config_change
	Message   string                 `json:"message"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// ClientInfo represents a connected NATS client
type ClientInfo struct {
	ClientID    string    `json:"client_id"`
	ConnectedAt time.Time `json:"connected_at"`
}

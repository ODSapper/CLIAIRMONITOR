# CLIAIRMONITOR NATS Integration Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Port the NATS wrapper, message types, and handler framework from CLIAIMONITOR to CLIAIRMONITOR to standardize agent communication.

**Architecture:** Create `internal/nats/` package with client wrapper, message definitions, and handler framework. Update the existing `aider/bridge.go` to use the new wrapper instead of raw `nats.Conn`. Remove duplicate message type definitions.

**Tech Stack:** Go 1.25+, github.com/nats-io/nats.go v1.47.0 (already in go.mod)

---

## Pre-Task: Cleanup

### Task 0: Delete Abandoned CLIAIR Directory

**Context:** There's an abandoned `CLIAIR` directory with an empty git repo (no commits, no code, no remote). This is dead weight.

**Step 1: Verify CLIAIR is empty**

```bash
cd "C:\Users\Admin\Documents\VS Projects"
dir CLIAIR
```
Expected: Empty directories only, no source files

**Step 2: Delete CLIAIR directory**

```bash
rmdir /s /q "C:\Users\Admin\Documents\VS Projects\CLIAIR"
```
Expected: Directory removed

---

## Task 1: Create NATS Client Wrapper

**Files:**
- Create: `internal/nats/client.go`
- Test: `internal/nats/client_test.go`

**Step 1: Create the nats package directory**

```bash
mkdir "C:\Users\Admin\Documents\VS Projects\CLIAIRMONITOR\internal\nats"
```

**Step 2: Write client.go**

Create `internal/nats/client.go`:

```go
package nats

import (
	"encoding/json"
	"fmt"
	"time"

	nc "github.com/nats-io/nats.go"
)

// Message represents a NATS message with subject, reply, and data
type Message struct {
	Subject string
	Reply   string
	Data    []byte
}

// Client wraps a NATS connection with convenience methods
type Client struct {
	conn     *nc.Conn
	clientID string
}

// NewClient creates a new NATS client with reconnect handling
// clientID should follow the convention:
//   - "sergeant" for Sergeant process (orchestrator)
//   - "server" for Server process
//   - "agent-{configName}-{seq}" for agents (e.g., "agent-coder-1")
func NewClient(url string, clientID string) (*Client, error) {
	opts := []nc.Option{
		nc.Name(clientID),
		nc.ReconnectWait(2 * time.Second),
		nc.MaxReconnects(-1),
		nc.DisconnectErrHandler(func(conn *nc.Conn, err error) {
			if err != nil {
				fmt.Printf("[NATS] %s disconnected: %v\n", clientID, err)
			}
		}),
		nc.ReconnectHandler(func(conn *nc.Conn) {
			fmt.Printf("[NATS] %s reconnected to %s\n", clientID, conn.ConnectedUrl())
		}),
		nc.ClosedHandler(func(conn *nc.Conn) {
			fmt.Printf("[NATS] %s connection closed\n", clientID)
		}),
	}

	conn, err := nc.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	return &Client{conn: conn, clientID: clientID}, nil
}

// GetClientID returns the client ID for this connection
func (c *Client) GetClientID() string {
	return c.clientID
}

// Close closes the NATS connection
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// Publish publishes data to a subject
func (c *Client) Publish(subject string, data []byte) error {
	if err := c.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("failed to publish to %s: %w", subject, err)
	}
	return nil
}

// PublishJSON publishes a JSON-encoded message to a subject
func (c *Client) PublishJSON(subject string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return c.Publish(subject, data)
}

// Subscribe creates an asynchronous subscription
func (c *Client) Subscribe(subject string, handler func(*Message)) (*nc.Subscription, error) {
	sub, err := c.conn.Subscribe(subject, func(msg *nc.Msg) {
		handler(&Message{
			Subject: msg.Subject,
			Reply:   msg.Reply,
			Data:    msg.Data,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to %s: %w", subject, err)
	}
	return sub, nil
}

// Request sends a request and waits for a reply
func (c *Client) Request(subject string, data []byte, timeout time.Duration) (*Message, error) {
	msg, err := c.conn.Request(subject, data, timeout)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", subject, err)
	}
	return &Message{
		Subject: msg.Subject,
		Reply:   msg.Reply,
		Data:    msg.Data,
	}, nil
}

// RequestJSON sends a JSON request and decodes the JSON response
func (c *Client) RequestJSON(subject string, req interface{}, resp interface{}, timeout time.Duration) error {
	reqData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	msg, err := c.Request(subject, reqData, timeout)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(msg.Data, resp); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return nil
}

// QueueSubscribe creates a load-balanced queue subscription
func (c *Client) QueueSubscribe(subject, queue string, handler func(*Message)) (*nc.Subscription, error) {
	sub, err := c.conn.QueueSubscribe(subject, queue, func(msg *nc.Msg) {
		handler(&Message{
			Subject: msg.Subject,
			Reply:   msg.Reply,
			Data:    msg.Data,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to queue subscribe to %s: %w", subject, err)
	}
	return sub, nil
}

// Flush flushes the buffered data to the server
func (c *Client) Flush() error {
	if err := c.conn.Flush(); err != nil {
		return fmt.Errorf("flush failed: %w", err)
	}
	return nil
}

// IsConnected returns true if the client is connected
func (c *Client) IsConnected() bool {
	return c.conn != nil && c.conn.IsConnected()
}

// RawConn returns the underlying NATS connection for advanced use cases
func (c *Client) RawConn() *nc.Conn {
	return c.conn
}
```

**Step 3: Verify it compiles**

```bash
cd "C:\Users\Admin\Documents\VS Projects\CLIAIRMONITOR"
go build ./internal/nats/...
```
Expected: No errors

**Step 4: Commit**

```bash
git add internal/nats/client.go
git commit -m "feat(nats): add NATS client wrapper with convenience methods"
```

---

## Task 2: Create NATS Message Types

**Files:**
- Create: `internal/nats/messages.go`

**Step 1: Write messages.go**

Create `internal/nats/messages.go`:

```go
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
```

**Step 2: Verify it compiles**

```bash
cd "C:\Users\Admin\Documents\VS Projects\CLIAIRMONITOR"
go build ./internal/nats/...
```
Expected: No errors

**Step 3: Commit**

```bash
git add internal/nats/messages.go
git commit -m "feat(nats): add message types and subject constants"
```

---

## Task 3: Update Aider Bridge to Use New Wrapper

**Files:**
- Modify: `internal/aider/bridge.go`

**Step 1: Update imports**

In `internal/aider/bridge.go`, change the import from:
```go
"github.com/nats-io/nats.go"
```
to:
```go
natslib "github.com/CLIAIRMONITOR/internal/nats"
"github.com/nats-io/nats.go"
```

**Step 2: Update Bridge struct**

Change the `natsConn` field from `*nats.Conn` to `*natslib.Client`:

```go
type Bridge struct {
	agentID     string
	status      string
	currentTask string

	// Process I/O
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	// NATS connection - use wrapper client
	natsClient *natslib.Client
	connected  bool
	mu         sync.RWMutex

	// Control
	stopCh chan struct{}
}
```

**Step 3: Remove duplicate StatusMessage and CommandMessage**

Delete the local `StatusMessage` and `CommandMessage` structs from bridge.go (they now exist in `internal/nats/messages.go`).

**Step 4: Update NewBridge function**

Change signature and usage:
```go
func NewBridge(agentID string, client *natslib.Client, stdin io.WriteCloser, stdout, stderr io.ReadCloser) *Bridge {
	return &Bridge{
		agentID:    agentID,
		natsClient: client,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		status:     "starting",
		connected:  false,
		stopCh:     make(chan struct{}),
	}
}
```

**Step 5: Update Start() to use wrapper**

```go
func (b *Bridge) Start() error {
	// Subscribe to commands for this agent
	subject := fmt.Sprintf(natslib.SubjectAgentCommand, b.agentID)
	_, err := b.natsClient.Subscribe(subject, b.handleCommandMsg)
	if err != nil {
		return fmt.Errorf("failed to subscribe to commands: %w", err)
	}

	// Start output parsing goroutines
	go b.parseAiderOutput()
	go b.parseAiderErrors()

	// Mark as connected and publish initial status
	b.mu.Lock()
	b.connected = true
	b.status = "connected"
	b.currentTask = "Aider ready"
	b.mu.Unlock()

	b.publishStatus("connected", "Aider ready")

	log.Printf("[BRIDGE] Started for agent %s", b.agentID)
	return nil
}
```

**Step 6: Update handleCommand to use wrapper message type**

```go
func (b *Bridge) handleCommandMsg(msg *natslib.Message) {
	var cmd natslib.CommandMessage
	if err := json.Unmarshal(msg.Data, &cmd); err != nil {
		log.Printf("[BRIDGE] Failed to parse command: %v", err)
		return
	}
	b.processCommand(cmd)
}

func (b *Bridge) processCommand(cmd natslib.CommandMessage) {
	// ... existing command handling logic using cmd.Type and cmd.Payload
}
```

**Step 7: Update publishStatus to use wrapper**

```go
func (b *Bridge) publishStatus(status, task string) {
	msg := natslib.StatusMessage{
		AgentID:     b.agentID,
		Status:      status,
		CurrentTask: task,
		Timestamp:   time.Now(),
	}

	subject := fmt.Sprintf(natslib.SubjectAgentStatus, b.agentID)
	if err := b.natsClient.PublishJSON(subject, msg); err != nil {
		log.Printf("[BRIDGE] Failed to publish status: %v", err)
	}
}
```

**Step 8: Update spawner.go to create client and pass to Bridge**

In `internal/aider/spawner.go`, update the agent creation to use the new client:

```go
// Create NATS client for this agent
clientID := fmt.Sprintf("agent-%s-%d", config.Name, agentNum)
natsClient, err := natslib.NewClient(s.natsURL, clientID)
if err != nil {
	return fmt.Errorf("failed to create NATS client: %w", err)
}

bridge := NewBridge(agentID, natsClient, stdin, stdout, stderr)
```

**Step 9: Verify it compiles**

```bash
cd "C:\Users\Admin\Documents\VS Projects\CLIAIRMONITOR"
go build ./...
```
Expected: No errors

**Step 10: Commit**

```bash
git add internal/aider/bridge.go internal/aider/spawner.go
git commit -m "refactor(aider): use NATS client wrapper in bridge"
```

---

## Task 4: Update Main to Use Client Wrapper

**Files:**
- Modify: `cmd/cliairmonitor/main.go`

**Step 1: Update main.go to create server client**

After starting the embedded NATS server, create a client for the server process:

```go
// Connect server NATS client
natsClient, err := natslib.NewClient(natsURL, "server")
if err != nil {
	log.Fatalf("[MAIN] Failed to create server NATS client: %v", err)
}
defer natsClient.Close()

// Pass client to spawner instead of raw nats.Conn
spawner := aider.NewSpawner(basePath, natsClient, memDB, config)
```

**Step 2: Update Spawner to accept Client**

Update `internal/aider/spawner.go` `NewSpawner` function signature:

```go
func NewSpawner(basePath string, client *natslib.Client, memDB memory.MemoryDB, config *Config) *Spawner {
	return &Spawner{
		basePath:   basePath,
		natsClient: client,
		natsURL:    "", // Get from client if needed
		memDB:      memDB,
		config:     config,
		agents:     make(map[string]*AgentProcess),
	}
}
```

**Step 3: Verify it compiles**

```bash
cd "C:\Users\Admin\Documents\VS Projects\CLIAIRMONITOR"
go build ./cmd/cliairmonitor/
```
Expected: No errors

**Step 4: Commit**

```bash
git add cmd/cliairmonitor/main.go internal/aider/spawner.go
git commit -m "refactor(main): use NATS client wrapper throughout"
```

---

## Task 5: Test NATS Integration

**Step 1: Build and run**

```bash
cd "C:\Users\Admin\Documents\VS Projects\CLIAIRMONITOR"
go build -o cliairmonitor.exe ./cmd/cliairmonitor/
./cliairmonitor.exe
```

Expected: Server starts with NATS on port 4223

**Step 2: Verify NATS subjects work**

In another terminal, test publishing:
```bash
# If you have nats CLI installed:
nats pub agent.test.status '{"agent_id":"test","status":"idle","current_task":"testing","timestamp":"2025-12-03T00:00:00Z"}'
```

**Step 3: Commit final changes**

```bash
git add -A
git commit -m "feat: complete NATS integration with client wrapper"
git push origin master
```

---

## Summary

| Task | Description | Files |
|------|-------------|-------|
| 0 | Delete abandoned CLIAIR | Shell command |
| 1 | Create NATS client wrapper | `internal/nats/client.go` |
| 2 | Create message types | `internal/nats/messages.go` |
| 3 | Update bridge to use wrapper | `internal/aider/bridge.go`, `spawner.go` |
| 4 | Update main to use wrapper | `cmd/cliairmonitor/main.go`, `spawner.go` |
| 5 | Test integration | Manual testing |

**Total estimated commits:** 5-6

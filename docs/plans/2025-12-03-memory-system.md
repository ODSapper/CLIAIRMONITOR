# CLIAIRMONITOR Memory System Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the dual-database memory system (OperationalDB + LearningDB) for CLIAIRMONITOR to enable agent state management, task queuing, and RAG-style learning.

**Architecture:** Two SQLite databases - one for fast operational state (agents, tasks, sessions, messages) and one for learning memory (episodes, knowledge with embeddings, procedures). Uses LM Studio's embedding endpoint for vector search.

**Tech Stack:** Go 1.25.3, SQLite with modernc.org/sqlite (pure Go), LM Studio embeddings API, NATS for real-time updates

---

## Phase 1: OperationalDB Implementation

### Task 1: Create SQLite Schema for OperationalDB

**Files:**
- Create: `internal/memory/operational.go`
- Create: `internal/memory/schema_operational.sql`

**Step 1: Write the schema file**

Create `internal/memory/schema_operational.sql`:

```sql
-- Agents table
CREATE TABLE IF NOT EXISTS agents (
    agent_id TEXT PRIMARY KEY,
    agent_type TEXT NOT NULL,
    model TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'starting',
    current_task TEXT,
    project_path TEXT,
    session_id TEXT,
    pid INTEGER,
    heartbeat_at DATETIME,
    shutdown_flag INTEGER NOT NULL DEFAULT 0,
    shutdown_reason TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata TEXT
);

CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
CREATE INDEX IF NOT EXISTS idx_agents_session ON agents(session_id);

-- Tasks table
CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT,
    task_type TEXT,
    priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    assigned_to TEXT,
    project_path TEXT,
    progress TEXT,
    summary TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    metadata TEXT,
    FOREIGN KEY (assigned_to) REFERENCES agents(agent_id)
);

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_assigned ON tasks(assigned_to);
CREATE INDEX IF NOT EXISTS idx_tasks_priority ON tasks(priority DESC);

-- Sessions table
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    project_path TEXT,
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at DATETIME,
    tokens_used INTEGER NOT NULL DEFAULT 0,
    tasks_completed INTEGER NOT NULL DEFAULT 0,
    summary TEXT,
    FOREIGN KEY (agent_id) REFERENCES agents(agent_id)
);

CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id);

-- Messages table
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL,
    message_type TEXT NOT NULL,
    content TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    acked_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_messages_to ON messages(to_agent);
CREATE INDEX IF NOT EXISTS idx_messages_unacked ON messages(to_agent, acked_at) WHERE acked_at IS NULL;

-- Metrics table
CREATE TABLE IF NOT EXISTS metrics (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    metric_type TEXT NOT NULL,
    value REAL NOT NULL,
    timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (agent_id) REFERENCES agents(agent_id)
);

CREATE INDEX IF NOT EXISTS idx_metrics_agent ON metrics(agent_id, timestamp);
```

**Step 2: Run schema verification**

Run: `cat internal/memory/schema_operational.sql`
Expected: Schema file displays correctly

**Step 3: Commit schema**

```bash
git add internal/memory/schema_operational.sql
git commit -m "feat(memory): add operational database schema"
```

---

### Task 2: Implement OperationalDB Core

**Files:**
- Create: `internal/memory/operational.go`

**Step 1: Write the OperationalDB implementation**

Create `internal/memory/operational.go`:

```go
package memory

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema_operational.sql
var operationalSchema embed.FS

// SQLiteOperationalDB implements OperationalDB using SQLite
type SQLiteOperationalDB struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewOperationalDB creates a new SQLite-backed operational database
func NewOperationalDB(dbPath string) (*SQLiteOperationalDB, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings
	db.SetMaxOpenConns(1) // SQLite works best with single writer
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	odb := &SQLiteOperationalDB{db: db}

	// Initialize schema
	if err := odb.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return odb, nil
}

func (o *SQLiteOperationalDB) initSchema() error {
	schema, err := operationalSchema.ReadFile("schema_operational.sql")
	if err != nil {
		return fmt.Errorf("failed to read schema: %w", err)
	}

	_, err = o.db.Exec(string(schema))
	if err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	return nil
}

// Close closes the database connection
func (o *SQLiteOperationalDB) Close() error {
	return o.db.Close()
}

// ================================================
// Agent Methods
// ================================================

func (o *SQLiteOperationalDB) RegisterAgent(agent *AgentState) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	metadata, err := json.Marshal(agent.Metadata)
	if err != nil {
		metadata = []byte("{}")
	}

	_, err = o.db.Exec(`
		INSERT INTO agents (agent_id, agent_type, model, status, current_task, project_path, session_id, pid, shutdown_flag, created_at, updated_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, agent.AgentID, agent.AgentType, agent.Model, string(agent.Status), agent.CurrentTask, agent.ProjectPath, agent.SessionID, agent.PID, agent.ShutdownFlag, agent.CreatedAt, agent.UpdatedAt, string(metadata))

	return err
}

func (o *SQLiteOperationalDB) UpdateAgentStatus(agentID string, status AgentStatus, task string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	_, err := o.db.Exec(`
		UPDATE agents SET status = ?, current_task = ?, updated_at = ? WHERE agent_id = ?
	`, string(status), task, time.Now(), agentID)

	return err
}

func (o *SQLiteOperationalDB) GetAgent(agentID string) (*AgentState, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	row := o.db.QueryRow(`
		SELECT agent_id, agent_type, model, status, current_task, project_path, session_id, pid, heartbeat_at, shutdown_flag, shutdown_reason, created_at, updated_at, metadata
		FROM agents WHERE agent_id = ?
	`, agentID)

	return o.scanAgent(row)
}

func (o *SQLiteOperationalDB) scanAgent(row *sql.Row) (*AgentState, error) {
	var agent AgentState
	var status string
	var currentTask, projectPath, sessionID, shutdownReason sql.NullString
	var pid sql.NullInt64
	var heartbeatAt sql.NullTime
	var metadata string

	err := row.Scan(&agent.AgentID, &agent.AgentType, &agent.Model, &status, &currentTask, &projectPath, &sessionID, &pid, &heartbeatAt, &agent.ShutdownFlag, &shutdownReason, &agent.CreatedAt, &agent.UpdatedAt, &metadata)
	if err != nil {
		return nil, err
	}

	agent.Status = AgentStatus(status)
	if currentTask.Valid {
		agent.CurrentTask = currentTask.String
	}
	if projectPath.Valid {
		agent.ProjectPath = projectPath.String
	}
	if sessionID.Valid {
		agent.SessionID = sessionID.String
	}
	if pid.Valid {
		pidInt := int(pid.Int64)
		agent.PID = &pidInt
	}
	if heartbeatAt.Valid {
		agent.HeartbeatAt = &heartbeatAt.Time
	}
	if shutdownReason.Valid {
		agent.ShutdownReason = shutdownReason.String
	}

	if metadata != "" {
		json.Unmarshal([]byte(metadata), &agent.Metadata)
	}

	return &agent, nil
}

func (o *SQLiteOperationalDB) ListAgents(filter AgentFilter) ([]*AgentState, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	query := `SELECT agent_id, agent_type, model, status, current_task, project_path, session_id, pid, heartbeat_at, shutdown_flag, shutdown_reason, created_at, updated_at, metadata FROM agents WHERE 1=1`
	args := []interface{}{}

	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, string(filter.Status))
	}
	if filter.AgentType != "" {
		query += " AND agent_type = ?"
		args = append(args, filter.AgentType)
	}
	if filter.Active != nil && *filter.Active {
		query += " AND status NOT IN ('stopped', 'unreachable')"
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	rows, err := o.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*AgentState
	for rows.Next() {
		var agent AgentState
		var status string
		var currentTask, projectPath, sessionID, shutdownReason sql.NullString
		var pid sql.NullInt64
		var heartbeatAt sql.NullTime
		var metadata string

		err := rows.Scan(&agent.AgentID, &agent.AgentType, &agent.Model, &status, &currentTask, &projectPath, &sessionID, &pid, &heartbeatAt, &agent.ShutdownFlag, &shutdownReason, &agent.CreatedAt, &agent.UpdatedAt, &metadata)
		if err != nil {
			return nil, err
		}

		agent.Status = AgentStatus(status)
		if currentTask.Valid {
			agent.CurrentTask = currentTask.String
		}
		if projectPath.Valid {
			agent.ProjectPath = projectPath.String
		}
		if sessionID.Valid {
			agent.SessionID = sessionID.String
		}
		if pid.Valid {
			pidInt := int(pid.Int64)
			agent.PID = &pidInt
		}
		if heartbeatAt.Valid {
			agent.HeartbeatAt = &heartbeatAt.Time
		}
		if shutdownReason.Valid {
			agent.ShutdownReason = shutdownReason.String
		}

		if metadata != "" {
			json.Unmarshal([]byte(metadata), &agent.Metadata)
		}

		agents = append(agents, &agent)
	}

	return agents, nil
}

func (o *SQLiteOperationalDB) SetShutdownFlag(agentID string, reason string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	_, err := o.db.Exec(`
		UPDATE agents SET shutdown_flag = 1, shutdown_reason = ?, updated_at = ? WHERE agent_id = ?
	`, reason, time.Now(), agentID)

	return err
}

func (o *SQLiteOperationalDB) RecordHeartbeat(agentID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()
	_, err := o.db.Exec(`
		UPDATE agents SET heartbeat_at = ?, updated_at = ? WHERE agent_id = ?
	`, now, now, agentID)

	return err
}

func (o *SQLiteOperationalDB) MarkStopped(agentID string, reason string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	_, err := o.db.Exec(`
		UPDATE agents SET status = 'stopped', shutdown_reason = ?, updated_at = ? WHERE agent_id = ?
	`, reason, time.Now(), agentID)

	return err
}

func (o *SQLiteOperationalDB) CleanupStaleAgents(threshold time.Duration) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	cutoff := time.Now().Add(-threshold)
	result, err := o.db.Exec(`
		UPDATE agents SET status = 'unreachable'
		WHERE status NOT IN ('stopped', 'unreachable')
		AND heartbeat_at < ?
	`, cutoff)
	if err != nil {
		return 0, err
	}

	count, _ := result.RowsAffected()
	return int(count), nil
}

// ================================================
// Task Methods
// ================================================

func (o *SQLiteOperationalDB) CreateTask(task *Task) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if task.ID == "" {
		task.ID = uuid.New().String()
	}
	task.CreatedAt = time.Now()
	task.UpdatedAt = time.Now()

	metadata, _ := json.Marshal(task.Metadata)

	_, err := o.db.Exec(`
		INSERT INTO tasks (id, title, description, task_type, priority, status, assigned_to, project_path, progress, summary, created_at, updated_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.Title, task.Description, task.TaskType, task.Priority, string(task.Status), task.AssignedTo, task.ProjectPath, task.Progress, task.Summary, task.CreatedAt, task.UpdatedAt, string(metadata))

	return err
}

func (o *SQLiteOperationalDB) ClaimTask(taskID, agentID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	result, err := o.db.Exec(`
		UPDATE tasks SET assigned_to = ?, status = 'claimed', updated_at = ?
		WHERE id = ? AND status = 'pending'
	`, agentID, time.Now(), taskID)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("task not available for claiming")
	}

	return nil
}

func (o *SQLiteOperationalDB) UpdateTaskProgress(taskID string, status TaskStatus, note string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	_, err := o.db.Exec(`
		UPDATE tasks SET status = ?, progress = ?, updated_at = ? WHERE id = ?
	`, string(status), note, time.Now(), taskID)

	return err
}

func (o *SQLiteOperationalDB) CompleteTask(taskID, summary string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()
	_, err := o.db.Exec(`
		UPDATE tasks SET status = 'completed', summary = ?, completed_at = ?, updated_at = ? WHERE id = ?
	`, summary, now, now, taskID)

	return err
}

func (o *SQLiteOperationalDB) GetTask(taskID string) (*Task, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	row := o.db.QueryRow(`
		SELECT id, title, description, task_type, priority, status, assigned_to, project_path, progress, summary, created_at, updated_at, completed_at, metadata
		FROM tasks WHERE id = ?
	`, taskID)

	var task Task
	var status string
	var description, taskType, assignedTo, projectPath, progress, summary sql.NullString
	var completedAt sql.NullTime
	var metadata string

	err := row.Scan(&task.ID, &task.Title, &description, &taskType, &task.Priority, &status, &assignedTo, &projectPath, &progress, &summary, &task.CreatedAt, &task.UpdatedAt, &completedAt, &metadata)
	if err != nil {
		return nil, err
	}

	task.Status = TaskStatus(status)
	if description.Valid {
		task.Description = description.String
	}
	if taskType.Valid {
		task.TaskType = taskType.String
	}
	if assignedTo.Valid {
		task.AssignedTo = assignedTo.String
	}
	if projectPath.Valid {
		task.ProjectPath = projectPath.String
	}
	if progress.Valid {
		task.Progress = progress.String
	}
	if summary.Valid {
		task.Summary = summary.String
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}

	if metadata != "" {
		json.Unmarshal([]byte(metadata), &task.Metadata)
	}

	return &task, nil
}

func (o *SQLiteOperationalDB) ListTasks(filter TaskFilter) ([]*Task, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	query := `SELECT id, title, description, task_type, priority, status, assigned_to, project_path, progress, summary, created_at, updated_at, completed_at, metadata FROM tasks WHERE 1=1`
	args := []interface{}{}

	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, string(filter.Status))
	}
	if filter.AssignedTo != "" {
		query += " AND assigned_to = ?"
		args = append(args, filter.AssignedTo)
	}
	if filter.TaskType != "" {
		query += " AND task_type = ?"
		args = append(args, filter.TaskType)
	}
	if filter.Priority != nil {
		query += " AND priority >= ?"
		args = append(args, *filter.Priority)
	}

	query += " ORDER BY priority DESC, created_at ASC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	rows, err := o.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		var task Task
		var status string
		var description, taskType, assignedTo, projectPath, progress, summary sql.NullString
		var completedAt sql.NullTime
		var metadata string

		err := rows.Scan(&task.ID, &task.Title, &description, &taskType, &task.Priority, &status, &assignedTo, &projectPath, &progress, &summary, &task.CreatedAt, &task.UpdatedAt, &completedAt, &metadata)
		if err != nil {
			return nil, err
		}

		task.Status = TaskStatus(status)
		if description.Valid {
			task.Description = description.String
		}
		if taskType.Valid {
			task.TaskType = taskType.String
		}
		if assignedTo.Valid {
			task.AssignedTo = assignedTo.String
		}
		if projectPath.Valid {
			task.ProjectPath = projectPath.String
		}
		if progress.Valid {
			task.Progress = progress.String
		}
		if summary.Valid {
			task.Summary = summary.String
		}
		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}

		if metadata != "" {
			json.Unmarshal([]byte(metadata), &task.Metadata)
		}

		tasks = append(tasks, &task)
	}

	return tasks, nil
}

// ================================================
// Session Methods
// ================================================

func (o *SQLiteOperationalDB) CreateSession(session *Session) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if session.ID == "" {
		session.ID = uuid.New().String()
	}
	session.StartedAt = time.Now()

	_, err := o.db.Exec(`
		INSERT INTO sessions (id, agent_id, project_path, started_at, tokens_used, tasks_completed)
		VALUES (?, ?, ?, ?, ?, ?)
	`, session.ID, session.AgentID, session.ProjectPath, session.StartedAt, session.TokensUsed, session.TasksCompleted)

	return err
}

func (o *SQLiteOperationalDB) GetSession(sessionID string) (*Session, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	row := o.db.QueryRow(`
		SELECT id, agent_id, project_path, started_at, ended_at, tokens_used, tasks_completed, summary
		FROM sessions WHERE id = ?
	`, sessionID)

	var session Session
	var projectPath, summary sql.NullString
	var endedAt sql.NullTime

	err := row.Scan(&session.ID, &session.AgentID, &projectPath, &session.StartedAt, &endedAt, &session.TokensUsed, &session.TasksCompleted, &summary)
	if err != nil {
		return nil, err
	}

	if projectPath.Valid {
		session.ProjectPath = projectPath.String
	}
	if endedAt.Valid {
		session.EndedAt = &endedAt.Time
	}
	if summary.Valid {
		session.Summary = summary.String
	}

	return &session, nil
}

func (o *SQLiteOperationalDB) UpdateSession(session *Session) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	_, err := o.db.Exec(`
		UPDATE sessions SET ended_at = ?, tokens_used = ?, tasks_completed = ?, summary = ? WHERE id = ?
	`, session.EndedAt, session.TokensUsed, session.TasksCompleted, session.Summary, session.ID)

	return err
}

func (o *SQLiteOperationalDB) GetActiveSession(agentID string) (*Session, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	row := o.db.QueryRow(`
		SELECT id, agent_id, project_path, started_at, ended_at, tokens_used, tasks_completed, summary
		FROM sessions WHERE agent_id = ? AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1
	`, agentID)

	var session Session
	var projectPath, summary sql.NullString
	var endedAt sql.NullTime

	err := row.Scan(&session.ID, &session.AgentID, &projectPath, &session.StartedAt, &endedAt, &session.TokensUsed, &session.TasksCompleted, &summary)
	if err != nil {
		return nil, err
	}

	if projectPath.Valid {
		session.ProjectPath = projectPath.String
	}
	if endedAt.Valid {
		session.EndedAt = &endedAt.Time
	}
	if summary.Valid {
		session.Summary = summary.String
	}

	return &session, nil
}

// ================================================
// Message Methods
// ================================================

func (o *SQLiteOperationalDB) SendMessage(msg *Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	msg.CreatedAt = time.Now()

	_, err := o.db.Exec(`
		INSERT INTO messages (id, from_agent, to_agent, message_type, content, priority, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, msg.ID, msg.FromAgent, msg.ToAgent, msg.MessageType, msg.Content, msg.Priority, msg.CreatedAt)

	return err
}

func (o *SQLiteOperationalDB) GetMessages(agentID string, since time.Time) ([]*Message, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	rows, err := o.db.Query(`
		SELECT id, from_agent, to_agent, message_type, content, priority, created_at, acked_at
		FROM messages WHERE to_agent = ? AND created_at > ? ORDER BY priority DESC, created_at ASC
	`, agentID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		var msg Message
		var ackedAt sql.NullTime

		err := rows.Scan(&msg.ID, &msg.FromAgent, &msg.ToAgent, &msg.MessageType, &msg.Content, &msg.Priority, &msg.CreatedAt, &ackedAt)
		if err != nil {
			return nil, err
		}

		if ackedAt.Valid {
			msg.AckedAt = &ackedAt.Time
		}

		messages = append(messages, &msg)
	}

	return messages, nil
}

func (o *SQLiteOperationalDB) AcknowledgeMessage(msgID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	_, err := o.db.Exec(`UPDATE messages SET acked_at = ? WHERE id = ?`, time.Now(), msgID)
	return err
}

// ================================================
// Metric Methods
// ================================================

func (o *SQLiteOperationalDB) RecordMetric(metric *Metric) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if metric.ID == "" {
		metric.ID = uuid.New().String()
	}
	metric.Timestamp = time.Now()

	_, err := o.db.Exec(`
		INSERT INTO metrics (id, agent_id, metric_type, value, timestamp)
		VALUES (?, ?, ?, ?, ?)
	`, metric.ID, metric.AgentID, metric.MetricType, metric.Value, metric.Timestamp)

	return err
}

func (o *SQLiteOperationalDB) GetMetrics(agentID string, since time.Time) ([]*Metric, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()

	rows, err := o.db.Query(`
		SELECT id, agent_id, metric_type, value, timestamp
		FROM metrics WHERE agent_id = ? AND timestamp > ? ORDER BY timestamp DESC
	`, agentID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []*Metric
	for rows.Next() {
		var m Metric
		err := rows.Scan(&m.ID, &m.AgentID, &m.MetricType, &m.Value, &m.Timestamp)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, &m)
	}

	return metrics, nil
}
```

**Step 2: Verify file compiles**

Run: `cd "C:/Users/Admin/Documents/VS Projects/CLIAIRMONITOR" && go build ./internal/memory/...`
Expected: No errors

**Step 3: Commit**

```bash
git add internal/memory/operational.go internal/memory/schema_operational.sql
git commit -m "feat(memory): implement OperationalDB with SQLite"
```

---

### Task 3: Write OperationalDB Tests

**Files:**
- Create: `internal/memory/operational_test.go`

**Step 1: Write the test file**

Create `internal/memory/operational_test.go`:

```go
package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) (*SQLiteOperationalDB, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := NewOperationalDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to create test DB: %v", err)
	}

	return db, func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}
}

func TestRegisterAndGetAgent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	agent := &AgentState{
		AgentID:   "test-agent-1",
		AgentType: "aider",
		Model:     "qwen2.5-coder-7b",
		Status:    AgentStatusStarting,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := db.RegisterAgent(agent)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	retrieved, err := db.GetAgent("test-agent-1")
	if err != nil {
		t.Fatalf("GetAgent failed: %v", err)
	}

	if retrieved.AgentID != agent.AgentID {
		t.Errorf("Expected agent ID %s, got %s", agent.AgentID, retrieved.AgentID)
	}
	if retrieved.Status != AgentStatusStarting {
		t.Errorf("Expected status %s, got %s", AgentStatusStarting, retrieved.Status)
	}
}

func TestUpdateAgentStatus(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	agent := &AgentState{
		AgentID:   "test-agent-2",
		AgentType: "aider",
		Model:     "qwen2.5-coder-7b",
		Status:    AgentStatusStarting,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	db.RegisterAgent(agent)

	err := db.UpdateAgentStatus("test-agent-2", AgentStatusWorking, "Processing task")
	if err != nil {
		t.Fatalf("UpdateAgentStatus failed: %v", err)
	}

	retrieved, _ := db.GetAgent("test-agent-2")
	if retrieved.Status != AgentStatusWorking {
		t.Errorf("Expected status %s, got %s", AgentStatusWorking, retrieved.Status)
	}
	if retrieved.CurrentTask != "Processing task" {
		t.Errorf("Expected task 'Processing task', got '%s'", retrieved.CurrentTask)
	}
}

func TestListAgentsWithFilter(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create multiple agents
	for i := 0; i < 5; i++ {
		status := AgentStatusIdle
		if i%2 == 0 {
			status = AgentStatusWorking
		}
		agent := &AgentState{
			AgentID:   fmt.Sprintf("agent-%d", i),
			AgentType: "aider",
			Model:     "qwen2.5-coder-7b",
			Status:    status,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		db.RegisterAgent(agent)
	}

	// Filter by status
	agents, err := db.ListAgents(AgentFilter{Status: AgentStatusWorking})
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}

	if len(agents) != 3 {
		t.Errorf("Expected 3 working agents, got %d", len(agents))
	}
}

func TestTaskLifecycle(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Create agent first
	agent := &AgentState{
		AgentID:   "task-agent",
		AgentType: "aider",
		Model:     "qwen2.5-coder-7b",
		Status:    AgentStatusIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	db.RegisterAgent(agent)

	// Create task
	task := &Task{
		Title:       "Test Task",
		Description: "A test task",
		TaskType:    "coding",
		Priority:    1,
		Status:      TaskStatusPending,
	}

	err := db.CreateTask(task)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// Claim task
	err = db.ClaimTask(task.ID, "task-agent")
	if err != nil {
		t.Fatalf("ClaimTask failed: %v", err)
	}

	// Update progress
	err = db.UpdateTaskProgress(task.ID, TaskStatusInProgress, "Working on it")
	if err != nil {
		t.Fatalf("UpdateTaskProgress failed: %v", err)
	}

	// Complete task
	err = db.CompleteTask(task.ID, "Task completed successfully")
	if err != nil {
		t.Fatalf("CompleteTask failed: %v", err)
	}

	// Verify
	retrieved, _ := db.GetTask(task.ID)
	if retrieved.Status != TaskStatusCompleted {
		t.Errorf("Expected status %s, got %s", TaskStatusCompleted, retrieved.Status)
	}
	if retrieved.CompletedAt == nil {
		t.Error("Expected CompletedAt to be set")
	}
}

func TestSessionManagement(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	agent := &AgentState{
		AgentID:   "session-agent",
		AgentType: "aider",
		Model:     "qwen2.5-coder-7b",
		Status:    AgentStatusIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	db.RegisterAgent(agent)

	session := &Session{
		AgentID:     "session-agent",
		ProjectPath: "/test/project",
	}

	err := db.CreateSession(session)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	active, err := db.GetActiveSession("session-agent")
	if err != nil {
		t.Fatalf("GetActiveSession failed: %v", err)
	}

	if active.ID != session.ID {
		t.Errorf("Expected session ID %s, got %s", session.ID, active.ID)
	}
}

func TestMessaging(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	msg := &Message{
		FromAgent:   "agent-1",
		ToAgent:     "agent-2",
		MessageType: "task",
		Content:     "Please review this code",
		Priority:    1,
	}

	err := db.SendMessage(msg)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	messages, err := db.GetMessages("agent-2", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	err = db.AcknowledgeMessage(messages[0].ID)
	if err != nil {
		t.Fatalf("AcknowledgeMessage failed: %v", err)
	}
}
```

**Step 2: Add missing import**

The test needs `fmt` for Sprintf:

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)
```

**Step 3: Run tests**

Run: `cd "C:/Users/Admin/Documents/VS Projects/CLIAIRMONITOR" && go test ./internal/memory/... -v`
Expected: All tests pass

**Step 4: Commit**

```bash
git add internal/memory/operational_test.go
git commit -m "test(memory): add OperationalDB unit tests"
```

---

## Phase 2: LearningDB Implementation

### Task 4: Create LearningDB Schema

**Files:**
- Create: `internal/memory/schema_learning.sql`

**Step 1: Write the schema**

Create `internal/memory/schema_learning.sql`:

```sql
-- Episodes table (episodic memory)
CREATE TABLE IF NOT EXISTS episodes (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    content TEXT NOT NULL,
    context TEXT,
    outcome TEXT,
    timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    importance REAL NOT NULL DEFAULT 0.5
);

CREATE INDEX IF NOT EXISTS idx_episodes_session ON episodes(session_id);
CREATE INDEX IF NOT EXISTS idx_episodes_agent ON episodes(agent_id);
CREATE INDEX IF NOT EXISTS idx_episodes_timestamp ON episodes(timestamp);
CREATE INDEX IF NOT EXISTS idx_episodes_importance ON episodes(importance DESC);

-- Episode summaries
CREATE TABLE IF NOT EXISTS episode_summaries (
    session_id TEXT PRIMARY KEY,
    summary TEXT NOT NULL,
    key_events TEXT,
    lessons_learned TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Knowledge table (semantic memory with embeddings)
CREATE TABLE IF NOT EXISTS knowledge (
    id TEXT PRIMARY KEY,
    category TEXT NOT NULL,
    title TEXT NOT NULL,
    content TEXT NOT NULL,
    source TEXT,
    embedding BLOB,
    tags TEXT,
    use_count INTEGER NOT NULL DEFAULT 0,
    last_used DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_knowledge_category ON knowledge(category);
CREATE INDEX IF NOT EXISTS idx_knowledge_use_count ON knowledge(use_count DESC);

-- Procedures table (procedural memory)
CREATE TABLE IF NOT EXISTS procedures (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    task_type TEXT NOT NULL,
    steps TEXT NOT NULL,
    preconditions TEXT,
    success_count INTEGER NOT NULL DEFAULT 0,
    failure_count INTEGER NOT NULL DEFAULT 0,
    last_used DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_procedures_task_type ON procedures(task_type);
CREATE INDEX IF NOT EXISTS idx_procedures_success ON procedures(success_count DESC);
```

**Step 2: Commit**

```bash
git add internal/memory/schema_learning.sql
git commit -m "feat(memory): add learning database schema"
```

---

### Task 5: Implement LearningDB Core

**Files:**
- Create: `internal/memory/learning.go`

**Step 1: Write the LearningDB implementation**

Create `internal/memory/learning.go`:

```go
package memory

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema_learning.sql
var learningSchema embed.FS

// SQLiteLearningDB implements LearningDB using SQLite
type SQLiteLearningDB struct {
	db               *sql.DB
	mu               sync.RWMutex
	embeddingProvider EmbeddingProvider
}

// NewLearningDB creates a new SQLite-backed learning database
func NewLearningDB(dbPath string) (*SQLiteLearningDB, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ldb := &SQLiteLearningDB{db: db}

	if err := ldb.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return ldb, nil
}

func (l *SQLiteLearningDB) initSchema() error {
	schema, err := learningSchema.ReadFile("schema_learning.sql")
	if err != nil {
		return fmt.Errorf("failed to read schema: %w", err)
	}

	_, err = l.db.Exec(string(schema))
	if err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	return nil
}

func (l *SQLiteLearningDB) Close() error {
	return l.db.Close()
}

func (l *SQLiteLearningDB) SetEmbeddingProvider(provider EmbeddingProvider) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.embeddingProvider = provider
	return nil
}

func (l *SQLiteLearningDB) ComputeEmbedding(text string) ([]float32, error) {
	l.mu.RLock()
	provider := l.embeddingProvider
	l.mu.RUnlock()

	if provider == nil {
		return nil, fmt.Errorf("no embedding provider configured")
	}

	return provider.Embed(text)
}

// ================================================
// Episode Methods
// ================================================

func (l *SQLiteLearningDB) RecordEpisode(episode *Episode) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if episode.ID == "" {
		episode.ID = uuid.New().String()
	}
	episode.Timestamp = time.Now()

	_, err := l.db.Exec(`
		INSERT INTO episodes (id, session_id, agent_id, event_type, content, context, outcome, timestamp, importance)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, episode.ID, episode.SessionID, episode.AgentID, episode.EventType, episode.Content, episode.Context, episode.Outcome, episode.Timestamp, episode.Importance)

	return err
}

func (l *SQLiteLearningDB) GetEpisodes(filter EpisodeFilter) ([]*Episode, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	query := `SELECT id, session_id, agent_id, event_type, content, context, outcome, timestamp, importance FROM episodes WHERE 1=1`
	args := []interface{}{}

	if filter.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, filter.SessionID)
	}
	if filter.AgentID != "" {
		query += " AND agent_id = ?"
		args = append(args, filter.AgentID)
	}
	if filter.EventType != "" {
		query += " AND event_type = ?"
		args = append(args, filter.EventType)
	}
	if !filter.Since.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, filter.Since)
	}
	if !filter.Until.IsZero() {
		query += " AND timestamp <= ?"
		args = append(args, filter.Until)
	}
	if filter.MinImportance > 0 {
		query += " AND importance >= ?"
		args = append(args, filter.MinImportance)
	}

	query += " ORDER BY timestamp DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := l.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var episodes []*Episode
	for rows.Next() {
		var ep Episode
		var context, outcome sql.NullString

		err := rows.Scan(&ep.ID, &ep.SessionID, &ep.AgentID, &ep.EventType, &ep.Content, &context, &outcome, &ep.Timestamp, &ep.Importance)
		if err != nil {
			return nil, err
		}

		if context.Valid {
			ep.Context = context.String
		}
		if outcome.Valid {
			ep.Outcome = outcome.String
		}

		episodes = append(episodes, &ep)
	}

	return episodes, nil
}

func (l *SQLiteLearningDB) SummarizeEpisodes(sessionID string) (*EpisodeSummary, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	row := l.db.QueryRow(`
		SELECT session_id, summary, key_events, lessons_learned, created_at
		FROM episode_summaries WHERE session_id = ?
	`, sessionID)

	var summary EpisodeSummary
	var keyEventsJSON, lessonsJSON sql.NullString

	err := row.Scan(&summary.SessionID, &summary.Summary, &keyEventsJSON, &lessonsJSON, &summary.CreatedAt)
	if err != nil {
		return nil, err
	}

	if keyEventsJSON.Valid {
		json.Unmarshal([]byte(keyEventsJSON.String), &summary.KeyEvents)
	}
	if lessonsJSON.Valid {
		json.Unmarshal([]byte(lessonsJSON.String), &summary.LessonsLearned)
	}

	return &summary, nil
}

func (l *SQLiteLearningDB) PruneOldEpisodes(before time.Time) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	result, err := l.db.Exec(`DELETE FROM episodes WHERE timestamp < ? AND importance < 0.8`, before)
	if err != nil {
		return 0, err
	}

	count, _ := result.RowsAffected()
	return int(count), nil
}

// ================================================
// Knowledge Methods
// ================================================

func (l *SQLiteLearningDB) StoreKnowledge(knowledge *Knowledge) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if knowledge.ID == "" {
		knowledge.ID = uuid.New().String()
	}
	knowledge.CreatedAt = time.Now()
	knowledge.UpdatedAt = time.Now()

	tags, _ := json.Marshal(knowledge.Tags)
	embedding := encodeEmbedding(knowledge.Embedding)

	_, err := l.db.Exec(`
		INSERT INTO knowledge (id, category, title, content, source, embedding, tags, use_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, knowledge.ID, knowledge.Category, knowledge.Title, knowledge.Content, knowledge.Source, embedding, string(tags), knowledge.UseCount, knowledge.CreatedAt, knowledge.UpdatedAt)

	return err
}

func (l *SQLiteLearningDB) GetKnowledge(id string) (*Knowledge, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	row := l.db.QueryRow(`
		SELECT id, category, title, content, source, embedding, tags, use_count, last_used, created_at, updated_at
		FROM knowledge WHERE id = ?
	`, id)

	return l.scanKnowledge(row)
}

func (l *SQLiteLearningDB) scanKnowledge(row *sql.Row) (*Knowledge, error) {
	var k Knowledge
	var source sql.NullString
	var embedding []byte
	var tags string
	var lastUsed sql.NullTime

	err := row.Scan(&k.ID, &k.Category, &k.Title, &k.Content, &source, &embedding, &tags, &k.UseCount, &lastUsed, &k.CreatedAt, &k.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if source.Valid {
		k.Source = source.String
	}
	if embedding != nil {
		k.Embedding = decodeEmbedding(embedding)
	}
	if tags != "" {
		json.Unmarshal([]byte(tags), &k.Tags)
	}
	if lastUsed.Valid {
		k.LastUsed = &lastUsed.Time
	}

	return &k, nil
}

func (l *SQLiteLearningDB) UpdateKnowledge(knowledge *Knowledge) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	knowledge.UpdatedAt = time.Now()
	tags, _ := json.Marshal(knowledge.Tags)
	embedding := encodeEmbedding(knowledge.Embedding)

	_, err := l.db.Exec(`
		UPDATE knowledge SET category = ?, title = ?, content = ?, source = ?, embedding = ?, tags = ?, use_count = ?, last_used = ?, updated_at = ?
		WHERE id = ?
	`, knowledge.Category, knowledge.Title, knowledge.Content, knowledge.Source, embedding, string(tags), knowledge.UseCount, knowledge.LastUsed, knowledge.UpdatedAt, knowledge.ID)

	return err
}

func (l *SQLiteLearningDB) SearchKnowledge(query string, limit int) ([]*Knowledge, error) {
	// Simple text search for now - embedding search requires provider
	l.mu.RLock()
	defer l.mu.RUnlock()

	rows, err := l.db.Query(`
		SELECT id, category, title, content, source, embedding, tags, use_count, last_used, created_at, updated_at
		FROM knowledge
		WHERE title LIKE ? OR content LIKE ?
		ORDER BY use_count DESC
		LIMIT ?
	`, "%"+query+"%", "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return l.scanKnowledgeRows(rows)
}

func (l *SQLiteLearningDB) SearchByEmbedding(queryEmbedding []float32, limit int) ([]*Knowledge, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Fetch all knowledge with embeddings and compute similarity
	rows, err := l.db.Query(`
		SELECT id, category, title, content, source, embedding, tags, use_count, last_used, created_at, updated_at
		FROM knowledge WHERE embedding IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		knowledge *Knowledge
		score     float64
	}

	var results []scored
	for rows.Next() {
		var k Knowledge
		var source sql.NullString
		var embedding []byte
		var tags string
		var lastUsed sql.NullTime

		err := rows.Scan(&k.ID, &k.Category, &k.Title, &k.Content, &source, &embedding, &tags, &k.UseCount, &lastUsed, &k.CreatedAt, &k.UpdatedAt)
		if err != nil {
			continue
		}

		if source.Valid {
			k.Source = source.String
		}
		if embedding != nil {
			k.Embedding = decodeEmbedding(embedding)
		}
		if tags != "" {
			json.Unmarshal([]byte(tags), &k.Tags)
		}
		if lastUsed.Valid {
			k.LastUsed = &lastUsed.Time
		}

		if k.Embedding != nil {
			score := cosineSimilarity(queryEmbedding, k.Embedding)
			results = append(results, scored{&k, score})
		}
	}

	// Sort by score descending
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].score > results[i].score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	// Return top N
	var topK []*Knowledge
	for i := 0; i < limit && i < len(results); i++ {
		topK = append(topK, results[i].knowledge)
	}

	return topK, nil
}

func (l *SQLiteLearningDB) scanKnowledgeRows(rows *sql.Rows) ([]*Knowledge, error) {
	var results []*Knowledge
	for rows.Next() {
		var k Knowledge
		var source sql.NullString
		var embedding []byte
		var tags string
		var lastUsed sql.NullTime

		err := rows.Scan(&k.ID, &k.Category, &k.Title, &k.Content, &source, &embedding, &tags, &k.UseCount, &lastUsed, &k.CreatedAt, &k.UpdatedAt)
		if err != nil {
			return nil, err
		}

		if source.Valid {
			k.Source = source.String
		}
		if embedding != nil {
			k.Embedding = decodeEmbedding(embedding)
		}
		if tags != "" {
			json.Unmarshal([]byte(tags), &k.Tags)
		}
		if lastUsed.Valid {
			k.LastUsed = &lastUsed.Time
		}

		results = append(results, &k)
	}
	return results, nil
}

func (l *SQLiteLearningDB) CompactKnowledge() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	_, err := l.db.Exec("VACUUM")
	return err
}

// ================================================
// Procedure Methods
// ================================================

func (l *SQLiteLearningDB) StoreProcedure(procedure *Procedure) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if procedure.ID == "" {
		procedure.ID = uuid.New().String()
	}
	procedure.CreatedAt = time.Now()
	procedure.UpdatedAt = time.Now()

	steps, _ := json.Marshal(procedure.Steps)
	preconditions, _ := json.Marshal(procedure.Preconditions)

	_, err := l.db.Exec(`
		INSERT INTO procedures (id, name, description, task_type, steps, preconditions, success_count, failure_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, procedure.ID, procedure.Name, procedure.Description, procedure.TaskType, string(steps), string(preconditions), procedure.SuccessCount, procedure.FailureCount, procedure.CreatedAt, procedure.UpdatedAt)

	return err
}

func (l *SQLiteLearningDB) GetProcedure(id string) (*Procedure, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	row := l.db.QueryRow(`
		SELECT id, name, description, task_type, steps, preconditions, success_count, failure_count, last_used, created_at, updated_at
		FROM procedures WHERE id = ?
	`, id)

	return l.scanProcedure(row)
}

func (l *SQLiteLearningDB) scanProcedure(row *sql.Row) (*Procedure, error) {
	var p Procedure
	var description sql.NullString
	var steps, preconditions string
	var lastUsed sql.NullTime

	err := row.Scan(&p.ID, &p.Name, &description, &p.TaskType, &steps, &preconditions, &p.SuccessCount, &p.FailureCount, &lastUsed, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if description.Valid {
		p.Description = description.String
	}
	json.Unmarshal([]byte(steps), &p.Steps)
	json.Unmarshal([]byte(preconditions), &p.Preconditions)
	if lastUsed.Valid {
		p.LastUsed = &lastUsed.Time
	}

	return &p, nil
}

func (l *SQLiteLearningDB) FindProcedures(taskType string, context string) ([]*Procedure, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	rows, err := l.db.Query(`
		SELECT id, name, description, task_type, steps, preconditions, success_count, failure_count, last_used, created_at, updated_at
		FROM procedures
		WHERE task_type = ?
		ORDER BY success_count DESC
		LIMIT 5
	`, taskType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var procedures []*Procedure
	for rows.Next() {
		var p Procedure
		var description sql.NullString
		var steps, preconditions string
		var lastUsed sql.NullTime

		err := rows.Scan(&p.ID, &p.Name, &description, &p.TaskType, &steps, &preconditions, &p.SuccessCount, &p.FailureCount, &lastUsed, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			return nil, err
		}

		if description.Valid {
			p.Description = description.String
		}
		json.Unmarshal([]byte(steps), &p.Steps)
		json.Unmarshal([]byte(preconditions), &p.Preconditions)
		if lastUsed.Valid {
			p.LastUsed = &lastUsed.Time
		}

		procedures = append(procedures, &p)
	}

	return procedures, nil
}

func (l *SQLiteLearningDB) UpdateProcedureSuccess(id string, success bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var query string
	if success {
		query = `UPDATE procedures SET success_count = success_count + 1, last_used = ?, updated_at = ? WHERE id = ?`
	} else {
		query = `UPDATE procedures SET failure_count = failure_count + 1, last_used = ?, updated_at = ? WHERE id = ?`
	}

	now := time.Now()
	_, err := l.db.Exec(query, now, now, id)
	return err
}

// ================================================
// Embedding Helpers
// ================================================

func encodeEmbedding(embedding []float32) []byte {
	if embedding == nil {
		return nil
	}
	buf := new(bytes.Buffer)
	for _, v := range embedding {
		binary.Write(buf, binary.LittleEndian, v)
	}
	return buf.Bytes()
}

func decodeEmbedding(data []byte) []float32 {
	if data == nil {
		return nil
	}
	count := len(data) / 4
	embedding := make([]float32, count)
	buf := bytes.NewReader(data)
	for i := 0; i < count; i++ {
		binary.Read(buf, binary.LittleEndian, &embedding[i])
	}
	return embedding
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}
```

**Step 2: Verify file compiles**

Run: `cd "C:/Users/Admin/Documents/VS Projects/CLIAIRMONITOR" && go build ./internal/memory/...`
Expected: No errors

**Step 3: Commit**

```bash
git add internal/memory/learning.go internal/memory/schema_learning.sql
git commit -m "feat(memory): implement LearningDB with SQLite and vector search"
```

---

### Task 6: Implement LM Studio Embedding Provider

**Files:**
- Create: `internal/memory/embedding_lmstudio.go`

**Step 1: Write the embedding provider**

Create `internal/memory/embedding_lmstudio.go`:

```go
package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LMStudioEmbedding implements EmbeddingProvider using LM Studio's API
type LMStudioEmbedding struct {
	baseURL    string
	model      string
	client     *http.Client
	dimensions int
}

// NewLMStudioEmbedding creates a new LM Studio embedding provider
func NewLMStudioEmbedding(baseURL, model string) *LMStudioEmbedding {
	return &LMStudioEmbedding{
		baseURL: baseURL,
		model:   model,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		dimensions: 1536, // Default, will be updated on first call
	}
}

type embeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type embeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func (l *LMStudioEmbedding) Embed(text string) ([]float32, error) {
	req := embeddingRequest{
		Input: text,
		Model: l.model,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := l.client.Post(l.baseURL+"/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to call embedding API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API error: %s - %s", resp.Status, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	embedding := embResp.Data[0].Embedding
	l.dimensions = len(embedding)

	return embedding, nil
}

func (l *LMStudioEmbedding) EmbedBatch(texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		emb, err := l.Embed(text)
		if err != nil {
			return nil, fmt.Errorf("failed to embed text %d: %w", i, err)
		}
		results[i] = emb
	}
	return results, nil
}

func (l *LMStudioEmbedding) Dimensions() int {
	return l.dimensions
}
```

**Step 2: Verify file compiles**

Run: `cd "C:/Users/Admin/Documents/VS Projects/CLIAIRMONITOR" && go build ./internal/memory/...`
Expected: No errors

**Step 3: Commit**

```bash
git add internal/memory/embedding_lmstudio.go
git commit -m "feat(memory): add LM Studio embedding provider"
```

---

## Phase 3: Integration

### Task 7: Update go.mod Dependencies

**Files:**
- Modify: `go.mod`

**Step 1: Add required dependencies**

Run: `cd "C:/Users/Admin/Documents/VS Projects/CLIAIRMONITOR" && go get modernc.org/sqlite`
Expected: Package downloaded

**Step 2: Tidy dependencies**

Run: `cd "C:/Users/Admin/Documents/VS Projects/CLIAIRMONITOR" && go mod tidy`
Expected: go.mod and go.sum updated

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add modernc.org/sqlite for pure Go SQLite"
```

---

### Task 8: Integrate Memory System with Main

**Files:**
- Modify: `cmd/cliairmonitor/main.go`

**Step 1: Add memory initialization to main.go**

Add imports and initialization code to connect memory databases at startup.

**Step 2: Verify build**

Run: `cd "C:/Users/Admin/Documents/VS Projects/CLIAIRMONITOR" && go build -o cliairmonitor.exe ./cmd/cliairmonitor/`
Expected: Binary builds successfully

**Step 3: Run tests**

Run: `cd "C:/Users/Admin/Documents/VS Projects/CLIAIRMONITOR" && go test ./... -v`
Expected: All tests pass

**Step 4: Commit**

```bash
git add cmd/cliairmonitor/main.go
git commit -m "feat: integrate memory system with main application"
```

---

## Verification Checklist

- [ ] OperationalDB schema created
- [ ] OperationalDB implementation compiles
- [ ] OperationalDB tests pass
- [ ] LearningDB schema created
- [ ] LearningDB implementation compiles
- [ ] LM Studio embedding provider works
- [ ] All tests pass
- [ ] Binary builds successfully
- [ ] Memory databases created on startup

---

**Plan complete and saved to `docs/plans/2025-12-03-memory-system.md`. Two execution options:**

**1. Subagent-Driven (this session)** - I dispatch fresh subagent per task, review between tasks, fast iteration

**2. Parallel Session (separate)** - Open new session with executing-plans, batch execution with checkpoints

**Which approach?**

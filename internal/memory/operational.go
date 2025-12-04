package memory

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema_operational.sql
var schemaOperational string

// SQLiteOperationalDB implements OperationalDB using SQLite
type SQLiteOperationalDB struct {
	db *sql.DB
}

// NewSQLiteOperationalDB creates a new SQLite operational database
func NewSQLiteOperationalDB(path string) (*SQLiteOperationalDB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure SQLite for concurrent access
	db.SetMaxOpenConns(1) // SQLite handles concurrency better with single connection

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Set busy timeout
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}

	// Execute schema
	if _, err := db.Exec(schemaOperational); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to execute schema: %w", err)
	}

	return &SQLiteOperationalDB{db: db}, nil
}

// Close closes the database connection
func (s *SQLiteOperationalDB) Close() error {
	return s.db.Close()
}

// ================================================
// Agent Lifecycle
// ================================================

// RegisterAgent registers a new agent
func (s *SQLiteOperationalDB) RegisterAgent(agent *AgentState) error {
	if agent.AgentID == "" {
		agent.AgentID = uuid.New().String()
	}

	now := time.Now()
	agent.CreatedAt = now
	agent.UpdatedAt = now

	metadata, err := json.Marshal(agent.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	query := `
		INSERT INTO agents (
			agent_id, agent_type, model, status, current_task, project_path,
			session_id, pid, heartbeat_at, shutdown_flag, shutdown_reason,
			created_at, updated_at, metadata
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = s.db.Exec(query,
		agent.AgentID, agent.AgentType, agent.Model, agent.Status,
		agent.CurrentTask, agent.ProjectPath, agent.SessionID,
		agent.PID, agent.HeartbeatAt, boolToInt(agent.ShutdownFlag),
		agent.ShutdownReason, agent.CreatedAt, agent.UpdatedAt, string(metadata))

	return err
}

// UpdateAgentStatus updates an agent's status and current task
func (s *SQLiteOperationalDB) UpdateAgentStatus(agentID string, status AgentStatus, task string) error {
	query := `
		UPDATE agents
		SET status = ?, current_task = ?, updated_at = ?
		WHERE agent_id = ?
	`
	_, err := s.db.Exec(query, status, task, time.Now(), agentID)
	return err
}

// GetAgent retrieves an agent by ID
func (s *SQLiteOperationalDB) GetAgent(agentID string) (*AgentState, error) {
	query := `
		SELECT agent_id, agent_type, model, status, current_task, project_path,
			   session_id, pid, heartbeat_at, shutdown_flag, shutdown_reason,
			   created_at, updated_at, metadata
		FROM agents
		WHERE agent_id = ?
	`

	var agent AgentState
	var metadata sql.NullString
	var currentTask, projectPath, sessionID, shutdownReason sql.NullString
	var pid sql.NullInt64
	var heartbeatAt sql.NullTime
	var shutdownFlag int

	err := s.db.QueryRow(query, agentID).Scan(
		&agent.AgentID, &agent.AgentType, &agent.Model, &agent.Status,
		&currentTask, &projectPath, &sessionID, &pid, &heartbeatAt,
		&shutdownFlag, &shutdownReason, &agent.CreatedAt, &agent.UpdatedAt,
		&metadata)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	if err != nil {
		return nil, err
	}

	agent.CurrentTask = currentTask.String
	agent.ProjectPath = projectPath.String
	agent.SessionID = sessionID.String
	agent.ShutdownReason = shutdownReason.String
	agent.ShutdownFlag = intToBool(shutdownFlag)

	if pid.Valid {
		pidInt := int(pid.Int64)
		agent.PID = &pidInt
	}
	if heartbeatAt.Valid {
		agent.HeartbeatAt = &heartbeatAt.Time
	}

	if metadata.Valid && metadata.String != "" {
		if err := json.Unmarshal([]byte(metadata.String), &agent.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}

	return &agent, nil
}

// ListAgents lists agents matching the filter
func (s *SQLiteOperationalDB) ListAgents(filter AgentFilter) ([]*AgentState, error) {
	query := `
		SELECT agent_id, agent_type, model, status, current_task, project_path,
			   session_id, pid, heartbeat_at, shutdown_flag, shutdown_reason,
			   created_at, updated_at, metadata
		FROM agents
		WHERE 1=1
	`
	args := []interface{}{}

	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	if filter.AgentType != "" {
		query += " AND agent_type = ?"
		args = append(args, filter.AgentType)
	}
	if filter.Active != nil && *filter.Active {
		query += " AND status != 'stopped'"
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*AgentState
	for rows.Next() {
		var agent AgentState
		var metadata sql.NullString
		var currentTask, projectPath, sessionID, shutdownReason sql.NullString
		var pid sql.NullInt64
		var heartbeatAt sql.NullTime
		var shutdownFlag int

		err := rows.Scan(
			&agent.AgentID, &agent.AgentType, &agent.Model, &agent.Status,
			&currentTask, &projectPath, &sessionID, &pid, &heartbeatAt,
			&shutdownFlag, &shutdownReason, &agent.CreatedAt, &agent.UpdatedAt,
			&metadata)
		if err != nil {
			return nil, err
		}

		agent.CurrentTask = currentTask.String
		agent.ProjectPath = projectPath.String
		agent.SessionID = sessionID.String
		agent.ShutdownReason = shutdownReason.String
		agent.ShutdownFlag = intToBool(shutdownFlag)

		if pid.Valid {
			pidInt := int(pid.Int64)
			agent.PID = &pidInt
		}
		if heartbeatAt.Valid {
			agent.HeartbeatAt = &heartbeatAt.Time
		}

		if metadata.Valid && metadata.String != "" {
			if err := json.Unmarshal([]byte(metadata.String), &agent.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		}

		agents = append(agents, &agent)
	}

	return agents, rows.Err()
}

// SetShutdownFlag sets the shutdown flag for an agent
func (s *SQLiteOperationalDB) SetShutdownFlag(agentID string, reason string) error {
	query := `
		UPDATE agents
		SET shutdown_flag = 1, shutdown_reason = ?, updated_at = ?
		WHERE agent_id = ?
	`
	_, err := s.db.Exec(query, reason, time.Now(), agentID)
	return err
}

// RecordHeartbeat updates the heartbeat timestamp for an agent
func (s *SQLiteOperationalDB) RecordHeartbeat(agentID string) error {
	query := `
		UPDATE agents
		SET heartbeat_at = ?, updated_at = ?
		WHERE agent_id = ?
	`
	now := time.Now()
	_, err := s.db.Exec(query, now, now, agentID)
	return err
}

// MarkStopped marks an agent as stopped
func (s *SQLiteOperationalDB) MarkStopped(agentID string, reason string) error {
	query := `
		UPDATE agents
		SET status = 'stopped', shutdown_reason = ?, updated_at = ?
		WHERE agent_id = ?
	`
	_, err := s.db.Exec(query, reason, time.Now(), agentID)
	return err
}

// CleanupStaleAgents marks agents as unreachable if they haven't heartbeated
func (s *SQLiteOperationalDB) CleanupStaleAgents(threshold time.Duration) (int, error) {
	cutoff := time.Now().Add(-threshold)
	query := `
		UPDATE agents
		SET status = 'unreachable', updated_at = ?
		WHERE status NOT IN ('stopped', 'stopping', 'unreachable')
		  AND (heartbeat_at IS NULL OR heartbeat_at < ?)
	`
	result, err := s.db.Exec(query, time.Now(), cutoff)
	if err != nil {
		return 0, err
	}

	count, err := result.RowsAffected()
	return int(count), err
}

// ================================================
// Task Queue
// ================================================

// CreateTask creates a new task
func (s *SQLiteOperationalDB) CreateTask(task *Task) error {
	if task.ID == "" {
		task.ID = uuid.New().String()
	}

	now := time.Now()
	task.CreatedAt = now
	task.UpdatedAt = now

	metadata, err := json.Marshal(task.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	query := `
		INSERT INTO tasks (
			id, title, description, task_type, priority, status, assigned_to,
			project_path, progress, summary, created_at, updated_at,
			completed_at, metadata
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = s.db.Exec(query,
		task.ID, task.Title, task.Description, task.TaskType, task.Priority,
		task.Status, task.AssignedTo, task.ProjectPath, task.Progress,
		task.Summary, task.CreatedAt, task.UpdatedAt, task.CompletedAt,
		string(metadata))

	return err
}

// ClaimTask assigns a task to an agent
func (s *SQLiteOperationalDB) ClaimTask(taskID, agentID string) error {
	query := `
		UPDATE tasks
		SET status = 'claimed', assigned_to = ?, updated_at = ?
		WHERE id = ? AND status = 'pending'
	`
	result, err := s.db.Exec(query, agentID, time.Now(), taskID)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task not available: %s", taskID)
	}

	return nil
}

// UpdateTaskProgress updates a task's status and progress
func (s *SQLiteOperationalDB) UpdateTaskProgress(taskID string, status TaskStatus, note string) error {
	query := `
		UPDATE tasks
		SET status = ?, progress = ?, updated_at = ?
		WHERE id = ?
	`
	_, err := s.db.Exec(query, status, note, time.Now(), taskID)
	return err
}

// CompleteTask marks a task as completed
func (s *SQLiteOperationalDB) CompleteTask(taskID, summary string) error {
	now := time.Now()
	query := `
		UPDATE tasks
		SET status = 'completed', summary = ?, completed_at = ?, updated_at = ?
		WHERE id = ?
	`
	_, err := s.db.Exec(query, summary, now, now, taskID)
	return err
}

// GetTask retrieves a task by ID
func (s *SQLiteOperationalDB) GetTask(taskID string) (*Task, error) {
	query := `
		SELECT id, title, description, task_type, priority, status, assigned_to,
			   project_path, progress, summary, created_at, updated_at,
			   completed_at, metadata
		FROM tasks
		WHERE id = ?
	`

	var task Task
	var metadata sql.NullString
	var description, taskType, assignedTo, projectPath sql.NullString
	var progress, summary sql.NullString
	var completedAt sql.NullTime

	err := s.db.QueryRow(query, taskID).Scan(
		&task.ID, &task.Title, &description, &taskType, &task.Priority,
		&task.Status, &assignedTo, &projectPath, &progress, &summary,
		&task.CreatedAt, &task.UpdatedAt, &completedAt, &metadata)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	if err != nil {
		return nil, err
	}

	task.Description = description.String
	task.TaskType = taskType.String
	task.AssignedTo = assignedTo.String
	task.ProjectPath = projectPath.String
	task.Progress = progress.String
	task.Summary = summary.String

	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}

	if metadata.Valid && metadata.String != "" {
		if err := json.Unmarshal([]byte(metadata.String), &task.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}

	return &task, nil
}

// ListTasks lists tasks matching the filter
func (s *SQLiteOperationalDB) ListTasks(filter TaskFilter) ([]*Task, error) {
	query := `
		SELECT id, title, description, task_type, priority, status, assigned_to,
			   project_path, progress, summary, created_at, updated_at,
			   completed_at, metadata
		FROM tasks
		WHERE 1=1
	`
	args := []interface{}{}

	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
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
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		var task Task
		var metadata sql.NullString
		var description, taskType, assignedTo, projectPath sql.NullString
		var progress, summary sql.NullString
		var completedAt sql.NullTime

		err := rows.Scan(
			&task.ID, &task.Title, &description, &taskType, &task.Priority,
			&task.Status, &assignedTo, &projectPath, &progress, &summary,
			&task.CreatedAt, &task.UpdatedAt, &completedAt, &metadata)
		if err != nil {
			return nil, err
		}

		task.Description = description.String
		task.TaskType = taskType.String
		task.AssignedTo = assignedTo.String
		task.ProjectPath = projectPath.String
		task.Progress = progress.String
		task.Summary = summary.String

		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}

		if metadata.Valid && metadata.String != "" {
			if err := json.Unmarshal([]byte(metadata.String), &task.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		}

		tasks = append(tasks, &task)
	}

	return tasks, rows.Err()
}

// ================================================
// Session Management
// ================================================

// CreateSession creates a new session
func (s *SQLiteOperationalDB) CreateSession(session *Session) error {
	if session.ID == "" {
		session.ID = uuid.New().String()
	}

	session.StartedAt = time.Now()

	query := `
		INSERT INTO sessions (
			id, agent_id, project_path, started_at, ended_at, tokens_used,
			tasks_completed, summary
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query,
		session.ID, session.AgentID, session.ProjectPath, session.StartedAt,
		session.EndedAt, session.TokensUsed, session.TasksCompleted,
		session.Summary)

	return err
}

// GetSession retrieves a session by ID
func (s *SQLiteOperationalDB) GetSession(sessionID string) (*Session, error) {
	query := `
		SELECT id, agent_id, project_path, started_at, ended_at, tokens_used,
			   tasks_completed, summary
		FROM sessions
		WHERE id = ?
	`

	var session Session
	var projectPath, summary sql.NullString
	var endedAt sql.NullTime

	err := s.db.QueryRow(query, sessionID).Scan(
		&session.ID, &session.AgentID, &projectPath, &session.StartedAt,
		&endedAt, &session.TokensUsed, &session.TasksCompleted, &summary)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		return nil, err
	}

	session.ProjectPath = projectPath.String
	session.Summary = summary.String
	if endedAt.Valid {
		session.EndedAt = &endedAt.Time
	}

	return &session, nil
}

// UpdateSession updates a session
func (s *SQLiteOperationalDB) UpdateSession(session *Session) error {
	query := `
		UPDATE sessions
		SET ended_at = ?, tokens_used = ?, tasks_completed = ?, summary = ?
		WHERE id = ?
	`
	_, err := s.db.Exec(query,
		session.EndedAt, session.TokensUsed, session.TasksCompleted,
		session.Summary, session.ID)
	return err
}

// GetActiveSession retrieves the active session for an agent
func (s *SQLiteOperationalDB) GetActiveSession(agentID string) (*Session, error) {
	query := `
		SELECT id, agent_id, project_path, started_at, ended_at, tokens_used,
			   tasks_completed, summary
		FROM sessions
		WHERE agent_id = ? AND ended_at IS NULL
		ORDER BY started_at DESC
		LIMIT 1
	`

	var session Session
	var projectPath, summary sql.NullString
	var endedAt sql.NullTime

	err := s.db.QueryRow(query, agentID).Scan(
		&session.ID, &session.AgentID, &projectPath, &session.StartedAt,
		&endedAt, &session.TokensUsed, &session.TasksCompleted, &summary)

	if err == sql.ErrNoRows {
		return nil, nil // No active session
	}
	if err != nil {
		return nil, err
	}

	session.ProjectPath = projectPath.String
	session.Summary = summary.String
	if endedAt.Valid {
		session.EndedAt = &endedAt.Time
	}

	return &session, nil
}

// ================================================
// Communication Channels
// ================================================

// SendMessage sends a message to an agent
func (s *SQLiteOperationalDB) SendMessage(msg *Message) error {
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}

	msg.CreatedAt = time.Now()

	query := `
		INSERT INTO messages (
			id, from_agent, to_agent, message_type, content, priority,
			created_at, acked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query,
		msg.ID, msg.FromAgent, msg.ToAgent, msg.MessageType, msg.Content,
		msg.Priority, msg.CreatedAt, msg.AckedAt)

	return err
}

// GetMessages retrieves messages for an agent since a given time
func (s *SQLiteOperationalDB) GetMessages(agentID string, since time.Time) ([]*Message, error) {
	query := `
		SELECT id, from_agent, to_agent, message_type, content, priority,
			   created_at, acked_at
		FROM messages
		WHERE to_agent = ? AND created_at >= ?
		ORDER BY priority DESC, created_at ASC
	`

	rows, err := s.db.Query(query, agentID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		var msg Message
		var ackedAt sql.NullTime

		err := rows.Scan(
			&msg.ID, &msg.FromAgent, &msg.ToAgent, &msg.MessageType,
			&msg.Content, &msg.Priority, &msg.CreatedAt, &ackedAt)
		if err != nil {
			return nil, err
		}

		if ackedAt.Valid {
			msg.AckedAt = &ackedAt.Time
		}

		messages = append(messages, &msg)
	}

	return messages, rows.Err()
}

// AcknowledgeMessage marks a message as acknowledged
func (s *SQLiteOperationalDB) AcknowledgeMessage(msgID string) error {
	query := `
		UPDATE messages
		SET acked_at = ?
		WHERE id = ?
	`
	_, err := s.db.Exec(query, time.Now(), msgID)
	return err
}

// ================================================
// Health and Metrics
// ================================================

// RecordMetric records a metric
func (s *SQLiteOperationalDB) RecordMetric(metric *Metric) error {
	if metric.ID == "" {
		metric.ID = uuid.New().String()
	}

	metric.Timestamp = time.Now()

	query := `
		INSERT INTO metrics (id, agent_id, metric_type, value, timestamp)
		VALUES (?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query,
		metric.ID, metric.AgentID, metric.MetricType, metric.Value,
		metric.Timestamp)

	return err
}

// GetMetrics retrieves metrics for an agent since a given time
func (s *SQLiteOperationalDB) GetMetrics(agentID string, since time.Time) ([]*Metric, error) {
	query := `
		SELECT id, agent_id, metric_type, value, timestamp
		FROM metrics
		WHERE agent_id = ? AND timestamp >= ?
		ORDER BY timestamp ASC
	`

	rows, err := s.db.Query(query, agentID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []*Metric
	for rows.Next() {
		var metric Metric

		err := rows.Scan(
			&metric.ID, &metric.AgentID, &metric.MetricType,
			&metric.Value, &metric.Timestamp)
		if err != nil {
			return nil, err
		}

		metrics = append(metrics, &metric)
	}

	return metrics, rows.Err()
}

// ================================================
// Utility Functions
// ================================================

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intToBool(i int) bool {
	return i != 0
}

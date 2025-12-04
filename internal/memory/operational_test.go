package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) (*SQLiteOperationalDB, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := NewSQLiteOperationalDB(dbPath)
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

func TestMetrics(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	agent := &AgentState{
		AgentID:   "metrics-agent",
		AgentType: "aider",
		Model:     "qwen2.5-coder-7b",
		Status:    AgentStatusIdle,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	db.RegisterAgent(agent)

	metric := &Metric{
		AgentID:    "metrics-agent",
		MetricType: "tokens",
		Value:      1500.0,
	}

	err := db.RecordMetric(metric)
	if err != nil {
		t.Fatalf("RecordMetric failed: %v", err)
	}

	metrics, err := db.GetMetrics("metrics-agent", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetMetrics failed: %v", err)
	}

	if len(metrics) != 1 {
		t.Fatalf("Expected 1 metric, got %d", len(metrics))
	}
	if metrics[0].Value != 1500.0 {
		t.Errorf("Expected value 1500.0, got %f", metrics[0].Value)
	}
}

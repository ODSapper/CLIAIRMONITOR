package memory

import (
	"database/sql"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

//go:embed schema_learning.sql
var schemaLearning string

// SQLiteLearningDB implements LearningDB using SQLite with embedded schema.
type SQLiteLearningDB struct {
	db               *sql.DB
	embeddingProvider EmbeddingProvider
}

// NewSQLiteLearningDB creates a new learning database.
func NewSQLiteLearningDB(dbPath string) (*SQLiteLearningDB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open learning db: %w", err)
	}

	// Configure SQLite for performance
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA cache_size=-64000", // 64MB cache
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set pragma: %w", err)
		}
	}

	// Initialize schema
	if _, err := db.Exec(schemaLearning); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return &SQLiteLearningDB{
		db: db,
	}, nil
}

// ================================================
// Episodic Memory
// ================================================

// RecordEpisode stores a new episode in memory.
func (s *SQLiteLearningDB) RecordEpisode(episode *Episode) error {
	if episode.ID == "" {
		episode.ID = uuid.New().String()
	}
	if episode.Timestamp.IsZero() {
		episode.Timestamp = time.Now()
	}

	query := `
		INSERT INTO episodes (id, session_id, agent_id, event_type, content, context, outcome, timestamp, importance)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query,
		episode.ID,
		episode.SessionID,
		episode.AgentID,
		episode.EventType,
		episode.Content,
		episode.Context,
		episode.Outcome,
		episode.Timestamp,
		episode.Importance,
	)

	if err != nil {
		return fmt.Errorf("failed to record episode: %w", err)
	}
	return nil
}

// GetEpisodes retrieves episodes based on filter criteria.
func (s *SQLiteLearningDB) GetEpisodes(filter EpisodeFilter) ([]*Episode, error) {
	query := "SELECT id, session_id, agent_id, event_type, content, context, outcome, timestamp, importance FROM episodes WHERE 1=1"
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
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query episodes: %w", err)
	}
	defer rows.Close()

	episodes := []*Episode{}
	for rows.Next() {
		ep := &Episode{}
		var context, outcome sql.NullString
		err := rows.Scan(
			&ep.ID,
			&ep.SessionID,
			&ep.AgentID,
			&ep.EventType,
			&ep.Content,
			&context,
			&outcome,
			&ep.Timestamp,
			&ep.Importance,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan episode: %w", err)
		}
		if context.Valid {
			ep.Context = context.String
		}
		if outcome.Valid {
			ep.Outcome = outcome.String
		}
		episodes = append(episodes, ep)
	}

	return episodes, rows.Err()
}

// SummarizeEpisodes creates a summary of episodes for a session.
func (s *SQLiteLearningDB) SummarizeEpisodes(sessionID string) (*EpisodeSummary, error) {
	// Check if summary already exists
	var summary EpisodeSummary
	var keyEventsJSON, lessonsJSON sql.NullString

	query := "SELECT session_id, summary, key_events, lessons_learned, created_at FROM episode_summaries WHERE session_id = ?"
	err := s.db.QueryRow(query, sessionID).Scan(
		&summary.SessionID,
		&summary.Summary,
		&keyEventsJSON,
		&lessonsJSON,
		&summary.CreatedAt,
	)

	if err == nil {
		// Summary exists, decode JSON fields
		if keyEventsJSON.Valid {
			json.Unmarshal([]byte(keyEventsJSON.String), &summary.KeyEvents)
		}
		if lessonsJSON.Valid {
			json.Unmarshal([]byte(lessonsJSON.String), &summary.LessonsLearned)
		}
		return &summary, nil
	}

	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to check for existing summary: %w", err)
	}

	// No summary exists, create a basic one from episodes
	episodes, err := s.GetEpisodes(EpisodeFilter{
		SessionID: sessionID,
		Limit:     100,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get episodes: %w", err)
	}

	if len(episodes) == 0 {
		return &EpisodeSummary{
			SessionID:  sessionID,
			Summary:    "No episodes recorded",
			CreatedAt:  time.Now(),
		}, nil
	}

	// Build basic summary
	summary = EpisodeSummary{
		SessionID: sessionID,
		Summary:   fmt.Sprintf("Session with %d episodes", len(episodes)),
		KeyEvents: []string{},
		LessonsLearned: []string{},
		CreatedAt: time.Now(),
	}

	// Extract high-importance episodes as key events
	for _, ep := range episodes {
		if ep.Importance > 0.7 {
			summary.KeyEvents = append(summary.KeyEvents, ep.Content)
		}
		if ep.EventType == "error" && ep.Outcome != "" {
			summary.LessonsLearned = append(summary.LessonsLearned, ep.Outcome)
		}
	}

	// Store the summary
	keyEventsBytes, _ := json.Marshal(summary.KeyEvents)
	lessonsBytes, _ := json.Marshal(summary.LessonsLearned)

	insertQuery := `
		INSERT INTO episode_summaries (session_id, summary, key_events, lessons_learned, created_at)
		VALUES (?, ?, ?, ?, ?)
	`
	_, err = s.db.Exec(insertQuery, summary.SessionID, summary.Summary, keyEventsBytes, lessonsBytes, summary.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to store summary: %w", err)
	}

	return &summary, nil
}

// PruneOldEpisodes removes episodes older than the specified time.
func (s *SQLiteLearningDB) PruneOldEpisodes(before time.Time) (int, error) {
	result, err := s.db.Exec("DELETE FROM episodes WHERE timestamp < ?", before)
	if err != nil {
		return 0, fmt.Errorf("failed to prune episodes: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get affected rows: %w", err)
	}

	return int(count), nil
}

// ================================================
// Semantic Memory (Knowledge)
// ================================================

// StoreKnowledge saves knowledge with optional embedding.
func (s *SQLiteLearningDB) StoreKnowledge(knowledge *Knowledge) error {
	if knowledge.ID == "" {
		knowledge.ID = uuid.New().String()
	}
	if knowledge.CreatedAt.IsZero() {
		knowledge.CreatedAt = time.Now()
	}
	if knowledge.UpdatedAt.IsZero() {
		knowledge.UpdatedAt = time.Now()
	}

	// Compute embedding if provider is set and embedding is empty
	if s.embeddingProvider != nil && len(knowledge.Embedding) == 0 {
		embedding, err := s.embeddingProvider.Embed(knowledge.Content)
		if err == nil {
			knowledge.Embedding = embedding
		}
	}

	var embeddingBlob []byte
	if len(knowledge.Embedding) > 0 {
		embeddingBlob = encodeEmbedding(knowledge.Embedding)
	}

	tagsJSON, _ := json.Marshal(knowledge.Tags)

	query := `
		INSERT INTO knowledge (id, category, title, content, source, embedding, tags, use_count, last_used, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			category = excluded.category,
			title = excluded.title,
			content = excluded.content,
			source = excluded.source,
			embedding = excluded.embedding,
			tags = excluded.tags,
			updated_at = excluded.updated_at
	`

	_, err := s.db.Exec(query,
		knowledge.ID,
		knowledge.Category,
		knowledge.Title,
		knowledge.Content,
		knowledge.Source,
		embeddingBlob,
		tagsJSON,
		knowledge.UseCount,
		knowledge.LastUsed,
		knowledge.CreatedAt,
		knowledge.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store knowledge: %w", err)
	}
	return nil
}

// GetKnowledge retrieves a knowledge item by ID.
func (s *SQLiteLearningDB) GetKnowledge(id string) (*Knowledge, error) {
	query := `
		SELECT id, category, title, content, source, embedding, tags, use_count, last_used, created_at, updated_at
		FROM knowledge WHERE id = ?
	`

	k := &Knowledge{}
	var embeddingBlob []byte
	var tagsJSON, source sql.NullString
	var lastUsed sql.NullTime

	err := s.db.QueryRow(query, id).Scan(
		&k.ID,
		&k.Category,
		&k.Title,
		&k.Content,
		&source,
		&embeddingBlob,
		&tagsJSON,
		&k.UseCount,
		&lastUsed,
		&k.CreatedAt,
		&k.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("knowledge not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get knowledge: %w", err)
	}

	if source.Valid {
		k.Source = source.String
	}
	if len(embeddingBlob) > 0 {
		k.Embedding = decodeEmbedding(embeddingBlob)
	}
	if tagsJSON.Valid {
		json.Unmarshal([]byte(tagsJSON.String), &k.Tags)
	}
	if lastUsed.Valid {
		k.LastUsed = &lastUsed.Time
	}

	return k, nil
}

// UpdateKnowledge updates an existing knowledge item.
func (s *SQLiteLearningDB) UpdateKnowledge(knowledge *Knowledge) error {
	knowledge.UpdatedAt = time.Now()

	var embeddingBlob []byte
	if len(knowledge.Embedding) > 0 {
		embeddingBlob = encodeEmbedding(knowledge.Embedding)
	}

	tagsJSON, _ := json.Marshal(knowledge.Tags)

	query := `
		UPDATE knowledge
		SET category = ?, title = ?, content = ?, source = ?, embedding = ?, tags = ?,
		    use_count = ?, last_used = ?, updated_at = ?
		WHERE id = ?
	`

	result, err := s.db.Exec(query,
		knowledge.Category,
		knowledge.Title,
		knowledge.Content,
		knowledge.Source,
		embeddingBlob,
		tagsJSON,
		knowledge.UseCount,
		knowledge.LastUsed,
		knowledge.UpdatedAt,
		knowledge.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update knowledge: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("knowledge not found: %s", knowledge.ID)
	}

	return nil
}

// SearchKnowledge searches by text query (falls back to simple text search if no embeddings).
func (s *SQLiteLearningDB) SearchKnowledge(query string, limit int) ([]*Knowledge, error) {
	// If embedding provider is available, compute embedding and use semantic search
	if s.embeddingProvider != nil {
		embedding, err := s.embeddingProvider.Embed(query)
		if err == nil {
			return s.SearchByEmbedding(embedding, limit)
		}
	}

	// Fall back to text search
	sqlQuery := `
		SELECT id, category, title, content, source, embedding, tags, use_count, last_used, created_at, updated_at
		FROM knowledge
		WHERE title LIKE ? OR content LIKE ? OR tags LIKE ?
		ORDER BY use_count DESC
		LIMIT ?
	`

	pattern := "%" + query + "%"
	rows, err := s.db.Query(sqlQuery, pattern, pattern, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search knowledge: %w", err)
	}
	defer rows.Close()

	return s.scanKnowledgeRows(rows)
}

// SearchByEmbedding performs semantic search using cosine similarity.
func (s *SQLiteLearningDB) SearchByEmbedding(embedding []float32, limit int) ([]*Knowledge, error) {
	// Get all knowledge items with embeddings
	query := `
		SELECT id, category, title, content, source, embedding, tags, use_count, last_used, created_at, updated_at
		FROM knowledge
		WHERE embedding IS NOT NULL
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query knowledge: %w", err)
	}
	defer rows.Close()

	type scoredKnowledge struct {
		knowledge *Knowledge
		score     float64
	}

	scored := []scoredKnowledge{}

	for rows.Next() {
		k := &Knowledge{}
		var embeddingBlob []byte
		var tagsJSON, source sql.NullString
		var lastUsed sql.NullTime

		err := rows.Scan(
			&k.ID,
			&k.Category,
			&k.Title,
			&k.Content,
			&source,
			&embeddingBlob,
			&tagsJSON,
			&k.UseCount,
			&lastUsed,
			&k.CreatedAt,
			&k.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan knowledge: %w", err)
		}

		if source.Valid {
			k.Source = source.String
		}
		if len(embeddingBlob) > 0 {
			k.Embedding = decodeEmbedding(embeddingBlob)
		}
		if tagsJSON.Valid {
			json.Unmarshal([]byte(tagsJSON.String), &k.Tags)
		}
		if lastUsed.Valid {
			k.LastUsed = &lastUsed.Time
		}

		// Compute similarity
		if len(k.Embedding) > 0 {
			similarity := cosineSimilarity(embedding, k.Embedding)
			scored = append(scored, scoredKnowledge{knowledge: k, score: similarity})
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating knowledge: %w", err)
	}

	// Sort by score descending
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	// Return top N
	if limit > len(scored) {
		limit = len(scored)
	}

	results := make([]*Knowledge, limit)
	for i := 0; i < limit; i++ {
		results[i] = scored[i].knowledge
	}

	return results, nil
}

// scanKnowledgeRows is a helper to scan multiple knowledge rows.
func (s *SQLiteLearningDB) scanKnowledgeRows(rows *sql.Rows) ([]*Knowledge, error) {
	results := []*Knowledge{}

	for rows.Next() {
		k := &Knowledge{}
		var embeddingBlob []byte
		var tagsJSON, source sql.NullString
		var lastUsed sql.NullTime

		err := rows.Scan(
			&k.ID,
			&k.Category,
			&k.Title,
			&k.Content,
			&source,
			&embeddingBlob,
			&tagsJSON,
			&k.UseCount,
			&lastUsed,
			&k.CreatedAt,
			&k.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan knowledge: %w", err)
		}

		if source.Valid {
			k.Source = source.String
		}
		if len(embeddingBlob) > 0 {
			k.Embedding = decodeEmbedding(embeddingBlob)
		}
		if tagsJSON.Valid {
			json.Unmarshal([]byte(tagsJSON.String), &k.Tags)
		}
		if lastUsed.Valid {
			k.LastUsed = &lastUsed.Time
		}

		results = append(results, k)
	}

	return results, rows.Err()
}

// ================================================
// Procedural Memory
// ================================================

// StoreProcedure saves a procedure workflow.
func (s *SQLiteLearningDB) StoreProcedure(procedure *Procedure) error {
	if procedure.ID == "" {
		procedure.ID = uuid.New().String()
	}
	if procedure.CreatedAt.IsZero() {
		procedure.CreatedAt = time.Now()
	}
	if procedure.UpdatedAt.IsZero() {
		procedure.UpdatedAt = time.Now()
	}

	stepsJSON, _ := json.Marshal(procedure.Steps)
	preconditionsJSON, _ := json.Marshal(procedure.Preconditions)

	query := `
		INSERT INTO procedures (id, name, description, task_type, steps, preconditions, success_count, failure_count, last_used, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			task_type = excluded.task_type,
			steps = excluded.steps,
			preconditions = excluded.preconditions,
			success_count = excluded.success_count,
			failure_count = excluded.failure_count,
			last_used = excluded.last_used,
			updated_at = excluded.updated_at
	`

	_, err := s.db.Exec(query,
		procedure.ID,
		procedure.Name,
		procedure.Description,
		procedure.TaskType,
		stepsJSON,
		preconditionsJSON,
		procedure.SuccessCount,
		procedure.FailureCount,
		procedure.LastUsed,
		procedure.CreatedAt,
		procedure.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store procedure: %w", err)
	}
	return nil
}

// GetProcedure retrieves a procedure by ID.
func (s *SQLiteLearningDB) GetProcedure(id string) (*Procedure, error) {
	query := `
		SELECT id, name, description, task_type, steps, preconditions, success_count, failure_count, last_used, created_at, updated_at
		FROM procedures WHERE id = ?
	`

	p := &Procedure{}
	var stepsJSON, preconditionsJSON, description sql.NullString
	var lastUsed sql.NullTime

	err := s.db.QueryRow(query, id).Scan(
		&p.ID,
		&p.Name,
		&description,
		&p.TaskType,
		&stepsJSON,
		&preconditionsJSON,
		&p.SuccessCount,
		&p.FailureCount,
		&lastUsed,
		&p.CreatedAt,
		&p.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("procedure not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get procedure: %w", err)
	}

	if description.Valid {
		p.Description = description.String
	}
	if stepsJSON.Valid {
		json.Unmarshal([]byte(stepsJSON.String), &p.Steps)
	}
	if preconditionsJSON.Valid {
		json.Unmarshal([]byte(preconditionsJSON.String), &p.Preconditions)
	}
	if lastUsed.Valid {
		p.LastUsed = &lastUsed.Time
	}

	return p, nil
}

// FindProcedures searches for procedures by task type and optional context.
func (s *SQLiteLearningDB) FindProcedures(taskType string, context string) ([]*Procedure, error) {
	query := `
		SELECT id, name, description, task_type, steps, preconditions, success_count, failure_count, last_used, created_at, updated_at
		FROM procedures
		WHERE task_type = ?
		ORDER BY success_count DESC, last_used DESC
	`

	rows, err := s.db.Query(query, taskType)
	if err != nil {
		return nil, fmt.Errorf("failed to find procedures: %w", err)
	}
	defer rows.Close()

	procedures := []*Procedure{}

	for rows.Next() {
		p := &Procedure{}
		var stepsJSON, preconditionsJSON, description sql.NullString
		var lastUsed sql.NullTime

		err := rows.Scan(
			&p.ID,
			&p.Name,
			&description,
			&p.TaskType,
			&stepsJSON,
			&preconditionsJSON,
			&p.SuccessCount,
			&p.FailureCount,
			&lastUsed,
			&p.CreatedAt,
			&p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan procedure: %w", err)
		}

		if description.Valid {
			p.Description = description.String
		}
		if stepsJSON.Valid {
			json.Unmarshal([]byte(stepsJSON.String), &p.Steps)
		}
		if preconditionsJSON.Valid {
			json.Unmarshal([]byte(preconditionsJSON.String), &p.Preconditions)
		}
		if lastUsed.Valid {
			p.LastUsed = &lastUsed.Time
		}

		// If context is provided, filter by matching preconditions or description
		if context != "" {
			if description.Valid && strings.Contains(strings.ToLower(description.String), strings.ToLower(context)) {
				procedures = append(procedures, p)
				continue
			}
			if preconditionsJSON.Valid && strings.Contains(strings.ToLower(preconditionsJSON.String), strings.ToLower(context)) {
				procedures = append(procedures, p)
				continue
			}
		} else {
			procedures = append(procedures, p)
		}
	}

	return procedures, rows.Err()
}

// UpdateProcedureSuccess updates the success/failure count of a procedure.
func (s *SQLiteLearningDB) UpdateProcedureSuccess(id string, success bool) error {
	now := time.Now()

	var query string
	if success {
		query = `
			UPDATE procedures
			SET success_count = success_count + 1, last_used = ?, updated_at = ?
			WHERE id = ?
		`
	} else {
		query = `
			UPDATE procedures
			SET failure_count = failure_count + 1, last_used = ?, updated_at = ?
			WHERE id = ?
		`
	}

	result, err := s.db.Exec(query, now, now, id)
	if err != nil {
		return fmt.Errorf("failed to update procedure: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("procedure not found: %s", id)
	}

	return nil
}

// ================================================
// Embedding Management
// ================================================

// ComputeEmbedding generates an embedding for text.
func (s *SQLiteLearningDB) ComputeEmbedding(text string) ([]float32, error) {
	if s.embeddingProvider == nil {
		return nil, fmt.Errorf("no embedding provider configured")
	}
	return s.embeddingProvider.Embed(text)
}

// SetEmbeddingProvider configures the embedding provider.
func (s *SQLiteLearningDB) SetEmbeddingProvider(provider EmbeddingProvider) error {
	s.embeddingProvider = provider
	return nil
}

// ================================================
// Maintenance
// ================================================

// CompactKnowledge removes duplicate or low-value knowledge.
func (s *SQLiteLearningDB) CompactKnowledge() error {
	// Remove knowledge that has never been used and is older than 30 days
	query := `
		DELETE FROM knowledge
		WHERE use_count = 0 AND created_at < datetime('now', '-30 days')
	`
	_, err := s.db.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to compact knowledge: %w", err)
	}

	// Vacuum to reclaim space
	_, err = s.db.Exec("VACUUM")
	if err != nil {
		return fmt.Errorf("failed to vacuum database: %w", err)
	}

	return nil
}

// Close closes the database connection.
func (s *SQLiteLearningDB) Close() error {
	return s.db.Close()
}

// ================================================
// Helper Functions
// ================================================

// encodeEmbedding converts []float32 to binary blob for storage.
func encodeEmbedding(embedding []float32) []byte {
	buf := make([]byte, len(embedding)*4)
	for i, val := range embedding {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(val))
	}
	return buf
}

// decodeEmbedding converts binary blob to []float32.
func decodeEmbedding(blob []byte) []float32 {
	if len(blob)%4 != 0 {
		return nil
	}
	embedding := make([]float32, len(blob)/4)
	for i := 0; i < len(embedding); i++ {
		bits := binary.LittleEndian.Uint32(blob[i*4:])
		embedding[i] = math.Float32frombits(bits)
	}
	return embedding
}

// cosineSimilarity computes cosine similarity between two embeddings.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := 0; i < len(a); i++ {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

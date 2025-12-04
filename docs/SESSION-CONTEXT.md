# CLIAIRMONITOR Session Context

## What This Project Is
- **CLIAIRMONITOR** (not CLIAIMONITOR - one letter different)
- Aider Sergeant with Qwen model via LM Studio
- Pure NATS messaging - NO MCP, NO HTTP heartbeats
- Module path: `github.com/CLIAIRMONITOR`

## Current State (2025-12-03)
- Binary builds successfully: `cliairmonitor.exe` (24MB)
- LM Studio config: `http://localhost:1234/v1` with `qwen2.5-coder-7b-instruct`
- Ports: HTTP 3001, NATS 4223

## Files Structure
```
cmd/cliairmonitor/main.go      - Entry point with HTTP API
configs/agents.yaml            - LM Studio configuration
internal/aider/
  bridge.go                    - NATS <-> Aider stdin/stdout
  config.go                    - Configuration types
  spawner.go                   - Aider process spawner
internal/memory/
  interfaces.go                - Interface definitions (needs implementation)
```

## Current Task: Implement Memory System
Plan saved at: `docs/plans/2025-12-03-memory-system.md`

### Tasks to Execute:
1. Create `schema_operational.sql` - Agents, tasks, sessions, messages tables
2. Create `operational.go` - OperationalDB implementation
3. Create `operational_test.go` - Tests
4. Create `schema_learning.sql` - Episodes, knowledge, procedures tables
5. Create `learning.go` - LearningDB with vector search
6. Create `embedding_lmstudio.go` - LM Studio embedding provider
7. Update go.mod with modernc.org/sqlite
8. Integrate with main.go

## Key Design Decisions
- Uses `modernc.org/sqlite` (pure Go, no CGO)
- WAL mode for SQLite
- Cosine similarity for vector search
- LM Studio `/embeddings` endpoint for vectors

## API Endpoints (current)
- GET /health
- GET /api/agents
- POST /api/agents/spawn?project=<path>
- POST /api/agents/stop?id=<agent-id>

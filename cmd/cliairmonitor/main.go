package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/CLIAIRMONITOR/internal/aider"
	"github.com/CLIAIRMONITOR/internal/memory"
	"github.com/nats-io/nats-server/v2/server"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "configs/agents.yaml", "Path to configuration file")
	port := flag.Int("port", 0, "Override server port (0 = use config)")
	flag.Parse()

	log.Println("===============================================")
	log.Println("  CLIAIRMONITOR - Aider Sergeant with Qwen")
	log.Println("===============================================")

	// Load configuration
	var config *aider.Config
	var err error

	if _, err := os.Stat(*configPath); err == nil {
		config, err = aider.LoadConfig(*configPath)
		if err != nil {
			log.Printf("[MAIN] Warning: Failed to load config from %s: %v", *configPath, err)
			log.Println("[MAIN] Using default configuration")
			config = aider.DefaultConfig()
		} else {
			log.Printf("[MAIN] Loaded configuration from %s", *configPath)
		}
	} else {
		log.Println("[MAIN] Config file not found, using defaults")
		config = aider.DefaultConfig()
	}

	// Override port if specified
	if *port > 0 {
		config.Server.Port = *port
	}

	log.Printf("[MAIN] Server port: %d", config.Server.Port)
	log.Printf("[MAIN] NATS port: %d", config.Server.NATSPort)
	log.Printf("[MAIN] LM Studio URL: %s", config.Ollama.URL)
	log.Printf("[MAIN] Model: %s", config.Ollama.Model)

	// Initialize memory databases
	dataDir := "data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("[MAIN] Failed to create data directory: %v", err)
	}

	operationalDB, err := memory.NewSQLiteOperationalDB(filepath.Join(dataDir, "operational.db"))
	if err != nil {
		log.Fatalf("[MAIN] Failed to initialize operational database: %v", err)
	}
	defer operationalDB.Close()

	learningDB, err := memory.NewSQLiteLearningDB(filepath.Join(dataDir, "learning.db"))
	if err != nil {
		log.Fatalf("[MAIN] Failed to initialize learning database: %v", err)
	}
	defer learningDB.Close()

	// Configure LM Studio embedding provider
	embeddingProvider := memory.NewLMStudioEmbedding("http://localhost:1234/v1", "qwen2.5-coder-7b-instruct")
	learningDB.SetEmbeddingProvider(embeddingProvider)

	log.Println("[MAIN] Memory system initialized (operational + learning databases)")

	// Start embedded NATS server
	natsOpts := &server.Options{
		Port:     config.Server.NATSPort,
		HTTPPort: -1, // Disable HTTP monitoring
		NoLog:    true,
		NoSigs:   true,
	}

	natsServer, err := server.NewServer(natsOpts)
	if err != nil {
		log.Fatalf("[MAIN] Failed to create NATS server: %v", err)
	}

	go natsServer.Start()

	// Wait for NATS to be ready
	if !natsServer.ReadyForConnections(5 * time.Second) {
		log.Fatal("[MAIN] NATS server failed to start in time")
	}
	log.Printf("[MAIN] Embedded NATS server started on port %d", config.Server.NATSPort)

	// Create NATS URL for spawner
	natsURL := fmt.Sprintf("nats://localhost:%d", config.Server.NATSPort)
	log.Printf("[MAIN] NATS URL for agents: %s", natsURL)

	// Create Aider spawner (it will create individual NATS clients for each agent)
	spawner := aider.NewSpawner(natsURL, config)
	log.Println("[MAIN] Aider spawner initialized")

	// Make databases available (can be used by handlers)
	_ = operationalDB
	_ = learningDB

	// Set up HTTP server for dashboard
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","agents":%d}`, len(spawner.ListAgents()))
	})

	// List agents endpoint
	mux.HandleFunc("/api/agents", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		agents := spawner.ListAgents()

		result := "["
		for i, agent := range agents {
			if i > 0 {
				result += ","
			}
			status, task := agent.Bridge.GetStatus()
			result += fmt.Sprintf(`{"id":"%s","project":"%s","model":"%s","status":"%s","task":"%s","uptime":"%s"}`,
				agent.ID, agent.ProjectPath, agent.Model, status, task, time.Since(agent.StartedAt).Round(time.Second))
		}
		result += "]"
		w.Write([]byte(result))
	})

	// Spawn agent endpoint
	mux.HandleFunc("/api/agents/spawn", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		projectPath := r.URL.Query().Get("project")
		if projectPath == "" {
			http.Error(w, "project parameter required", http.StatusBadRequest)
			return
		}

		agentConfig := aider.AgentConfig{
			Name:        "Qwen-Agent",
			Role:        "developer",
			ProjectPath: projectPath,
		}

		agent, err := spawner.SpawnAgent(agentConfig)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"%s","project":"%s","model":"%s"}`, agent.ID, agent.ProjectPath, agent.Model)
	})

	// Stop agent endpoint
	mux.HandleFunc("/api/agents/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		agentID := r.URL.Query().Get("id")
		if agentID == "" {
			http.Error(w, "id parameter required", http.StatusBadRequest)
			return
		}

		if err := spawner.StopAgent(agentID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"stopped","id":"%s"}`, agentID)
	})

	// Create HTTP server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", config.Server.Port),
		Handler: mux,
	}

	// Start HTTP server in background
	go func() {
		log.Printf("[MAIN] HTTP server starting on port %d", config.Server.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[MAIN] HTTP server error: %v", err)
		}
	}()

	log.Println("===============================================")
	log.Printf("  CLIAIRMONITOR ready!")
	log.Printf("  Dashboard: http://localhost:%d", config.Server.Port)
	log.Printf("  Health:    http://localhost:%d/health", config.Server.Port)
	log.Printf("  Agents:    http://localhost:%d/api/agents", config.Server.Port)
	log.Println("===============================================")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[MAIN] Shutdown signal received")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stop all agents first
	spawner.StopAll()

	// Shutdown HTTP server
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("[MAIN] HTTP server shutdown error: %v", err)
	}

	// Shutdown NATS server (agents have their own clients that will be closed)
	natsServer.Shutdown()

	log.Println("[MAIN] CLIAIRMONITOR shutdown complete")
}

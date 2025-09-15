package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/google/go-github/v74/github"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/conveyorci/conveyor/internal/pipeline"
	"github.com/conveyorci/conveyor/internal/shared"
	"github.com/conveyorci/conveyor/internal/store"
)

// Server holds dependencies like the database store.
type Server struct {
	store *store.Store
}

// NewServer creates a new server instance.
func NewServer(store *store.Store) *Server {
	return &Server{store: store}
}

// --- HTTP Handlers ---

func (s *Server) requestJobHandler(w http.ResponseWriter, r *http.Request) {
	jobReq, err := s.store.RequestJob()
	if err != nil {
		http.Error(w, "Failed to request job from store", http.StatusInternalServerError)
		log.Printf("ERROR: requestJobHandler: %v", err)
		return
	}

	if jobReq == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	log.Printf("Dispatching job %s to an agent", jobReq.ID)
	w.Header().Set("Content-Type", "application/json")
	err1 := json.NewEncoder(w).Encode(jobReq)
	if err1 != nil {
		return
	}
}

func (s *Server) updateJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Path[len("/api/jobs/update/"):]
	var update shared.JobStatusUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateJobStatus(jobID, update.Status, update.Error); err != nil {
		http.Error(w, "Failed to update job status", http.StatusInternalServerError)
		log.Printf("ERROR: updateJobHandler: %v", err)
		return
	}

	log.Printf("Received status update for job %s: %s", jobID, update.Status)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) githubWebhookHandler(w http.ResponseWriter, r *http.Request) {
	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	if secret == "" {
		log.Println("WARN: GITHUB_WEBHOOK_SECRET is not set. Cannot validate webhooks.")
		http.Error(w, "Webhook secret not configured", http.StatusInternalServerError)
		return
	}

	payload, err := github.ValidatePayload(r, []byte(secret))
	if err != nil {
		log.Printf("WARN: Invalid webhook payload: %v", err)
		http.Error(w, "Invalid payload", http.StatusUnauthorized)
		return
	}

	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		log.Printf("WARN: Could not parse webhook: %v", err)
		http.Error(w, "Could not parse webhook", http.StatusBadRequest)
		return
	}

	pushEvent, ok := event.(*github.PushEvent)
	if !ok {
		log.Println("Received webhook, but it was not a push event.")
		_, err := fmt.Fprint(w, "Event was not a push event, ignoring.")
		if err != nil {
			return
		}
		return
	}

	repoURL := *pushEvent.Repo.CloneURL
	log.Printf("Received push event for repo: %s", repoURL)

	// Create a temporary directory to clone the repo
	tempDir, err := os.MkdirTemp("", "conveyor-workspace-*")
	if err != nil {
		http.Error(w, "Failed to create temp workspace", http.StatusInternalServerError)
		return
	}
	defer func(path string) {
		err := os.RemoveAll(path)
		if err != nil {
			log.Printf("WARN: Failed to remove temp workspace: %v", err)
		}
	}(tempDir)

	// Clone the repo
	cmd := exec.Command("git", "clone", repoURL, tempDir)
	if err := cmd.Run(); err != nil {
		http.Error(w, "Failed to clone repository", http.StatusInternalServerError)
		return
	}

	// Read the .conveyor.yml from the cloned repo
	conveyorFile := filepath.Join(tempDir, ".conveyor.yml")
	data, err := os.ReadFile(conveyorFile)
	if err != nil {
		http.Error(w, "Could not read .conveyor.yml from repository", http.StatusBadRequest)
		return
	}

	// Parse and queue the jobs
	var p pipeline.Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		http.Error(w, "Could not parse .conveyor.yml", http.StatusBadRequest)
		return
	}

	for jobName, job := range p.Jobs {
		jobID := uuid.New().String()
		if err := s.store.QueueJob(jobID, job); err != nil {
			log.Printf("ERROR: Failed to queue job '%s': %v", jobName, err)
			continue
		}
		log.Printf("Queued job '%s' with ID %s from webhook", jobName, jobID)
	}

	_, err1 := fmt.Fprintf(w, "Webhook processed. Queued %d jobs.\n", len(p.Jobs))
	if err1 != nil {
		return
	}
}

// --- Main Function ---

func main() {
	db, err := store.NewStore("conveyor.db")
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	server := NewServer(db)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/jobs/request", server.requestJobHandler)
	mux.HandleFunc("/api/jobs/update/", server.updateJobHandler)
	mux.HandleFunc("/webhooks/github", server.githubWebhookHandler)

	staticFS := getStaticFS()
	mux.Handle("/", http.FileServer(staticFS))

	port := "8080"
	log.Printf("Starting Conveyor Server on port %s", port)
	log.Println("Access the UI at http://localhost:8080")

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

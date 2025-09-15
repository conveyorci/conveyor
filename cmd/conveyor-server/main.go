package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/conveyorci/conveyor/internal/pipeline"
	"github.com/conveyorci/conveyor/internal/shared"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// TODO: switch to database
// --- Server State (In-Memory) ---

// jobStore holds the details of all jobs, protected by a mutex.
var jobStore = struct {
	sync.RWMutex
	jobs map[string]shared.JobRequest
}{jobs: make(map[string]shared.JobRequest)}

// jobQueue is a simple channel acting as in-memory job queue.
var jobQueue = make(chan shared.JobRequest, 100) // Buffer of 100 jobs

// --- HTTP Handlers ---

// requestJobHandler is called by agents asking for work.
func requestJobHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET is allowed", http.StatusMethodNotAllowed)
		return
	}

	select {
	case jobReq := <-jobQueue:
		log.Printf("Dispatching job %s to an agent", jobReq.ID)
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(jobReq)
		if err != nil {
			return
		}
	default:
		// No jobs in the queue
		w.WriteHeader(http.StatusNoContent)
	}
}

// updateJobHandler is called by agents to report status.
func updateJobHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST is allowed", http.StatusMethodNotAllowed)
		return
	}

	jobID := r.URL.Path[len("/api/jobs/update/"):]
	var update shared.JobStatusUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Received status update for job %s: %s", jobID, update.Status)
	if update.Status == shared.StatusFailed {
		log.Printf("Job %s failed with error: %s", jobID, update.Error)
	}

	// TODO: switch to database, reference to first TODO here
	w.WriteHeader(http.StatusOK)
}

// triggerBuildHandler is a temporary endpoint to manually start a build for testing.
func triggerBuildHandler(w http.ResponseWriter, r *http.Request) {
	log.Println("Received trigger request, queuing jobs from .conveyor.yml")

	data, err := os.ReadFile(".conveyor.yml")
	if err != nil {
		http.Error(w, "Could not read .conveyor.yml", http.StatusInternalServerError)
		return
	}

	var p pipeline.Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		http.Error(w, "Could not parse .conveyor.yml", http.StatusInternalServerError)
		return
	}

	for jobName, job := range p.Jobs {
		jobID := uuid.New().String()
		jobReq := shared.JobRequest{
			ID:  jobID,
			Job: job,
		}

		jobStore.Lock()
		jobStore.jobs[jobID] = jobReq
		jobStore.Unlock()

		jobQueue <- jobReq
		log.Printf("Queued job '%s' with ID %s", jobName, jobID)
	}

	_, err = fmt.Fprintf(w, "Successfully queued %d jobs.\n", len(p.Jobs))
	if err != nil {
		return
	}
}

// --- Main Function ---

func main() {
	http.HandleFunc("/api/jobs/request", requestJobHandler)
	http.HandleFunc("/api/jobs/update/", updateJobHandler)
	http.HandleFunc("/api/trigger", triggerBuildHandler) // test endpoint

	port := "8080"
	log.Printf("Starting Conveyor Server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

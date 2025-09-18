package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/conveyorci/conveyor/internal/runner"
	"github.com/conveyorci/conveyor/internal/shared"
	"github.com/spf13/pflag"
)

// Agent holds the configuration and state for a Conveyor agent.
type Agent struct {
	serverURL  string
	token      string // for future authentication with the server
	httpClient *http.Client
	executor   *runner.DockerExecutor
}

// NewAgent creates and configures a new agent.
func NewAgent(serverURL, token string) (*Agent, error) {
	executor, err := runner.NewDockerExecutor()
	if err != nil {
		return nil, fmt.Errorf("could not create docker executor: %w", err)
	}

	return &Agent{
		serverURL: serverURL,
		token:     token,
		httpClient: &http.Client{
			Timeout: 15 * time.Second, // add a timeout to HTTP requests
		},
		executor: executor,
	}, nil
}

// Run starts the agent's main polling loop.
func (a *Agent) Run(ctx context.Context) {
	log.Println("Starting agent loop...")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down agent...")
			return
		case <-ticker.C:
			log.Println("Polling for a new job...")
			jobReq, err := a.requestJob()
			if err != nil {
				log.Printf("Error requesting job: %v", err)
				// TODO: Implement smarter backoff later if needed
				time.Sleep(10 * time.Second)
				continue
			}

			if jobReq == nil {
				continue // No job available, wait for next tick
			}

			a.processJob(jobReq)
		}
	}
}

// processJob handles the full lifecycle of a single job.
func (a *Agent) processJob(jobReq *shared.JobRequest) {
	log.Printf("Picked up job %s for repo %s", jobReq.ID, jobReq.RepoName)

	if err := a.updateJobStatus(jobReq.ID, shared.JobStatusUpdate{Status: shared.StatusRunning}); err != nil {
		log.Printf("ERROR: Failed to update job status to 'running' for job %s: %v", jobReq.ID, err)
		return
	}

	// TODO: make real-time logs and then via `logWriter` could be a WebSocket connection.
	logWriter := os.Stdout

	workspace, err := os.Getwd()
	if err != nil {
		log.Printf("FATAL: Could not get working directory: %v", err)
		a.updateJobStatus(jobReq.ID, shared.JobStatusUpdate{
			Status: shared.StatusFailed,
			Error:  "Agent failed to get working directory",
		})
		return
	}

	err = a.executor.Execute(context.Background(), jobReq.Job, workspace, logWriter)

	if err != nil {
		log.Printf("Job %s failed: %v", jobReq.ID, err)
		a.updateJobStatus(jobReq.ID, shared.JobStatusUpdate{
			Status: shared.StatusFailed,
			Error:  err.Error(),
		})
	} else {
		log.Printf("Job %s completed successfully", jobReq.ID)
		a.updateJobStatus(jobReq.ID, shared.JobStatusUpdate{Status: shared.StatusSuccess})
	}
}

func (a *Agent) requestJob() (*shared.JobRequest, error) {
	req, err := http.NewRequest(http.MethodGet, a.serverURL+"/api/jobs/request", nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request: %w", err)
	}
	// TODO: Add token to header: req.Header.Set("Authorization", "Bearer "+a.token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not request job from server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned non-200 status: %s", resp.Status)
	}

	var jobReq shared.JobRequest
	if err := json.NewDecoder(resp.Body).Decode(&jobReq); err != nil {
		return nil, fmt.Errorf("could not decode job request: %w", err)
	}
	return &jobReq, nil
}

func (a *Agent) updateJobStatus(jobID string, update shared.JobStatusUpdate) error {
	data, err := json.Marshal(update)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, a.serverURL+"/api/jobs/update/"+jobID, bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// TODO: Add token to header

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned non-200 status for update: %s", resp.Status)
	}
	return nil
}

func main() {
	serverURL := pflag.String("server-url", "http://localhost:8080", "The URL of the Conveyor server.")
	token := pflag.String("token", "", "The agent registration token.")
	pflag.Parse()

	if *token == "" {
		log.Println("WARN: No agent token provided. This will be required in a future version.")
	}

	log.Printf("Starting Conveyor Agent, connecting to server at %s", *serverURL)

	agent, err := NewAgent(*serverURL, *token)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Shutdown signal received, stopping agent...")
		cancel()
	}()

	agent.Run(ctx)
	log.Println("Agent has shut down.")
}

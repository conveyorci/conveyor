package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
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

	// job
	execErr := a.executor.Execute(context.Background(), jobReq.Job, workspace, logWriter)

	// After execution, check if there are artifacts to process, but only if the job succeeded.
	if execErr == nil && len(jobReq.Job.Artifacts.Paths) > 0 {
		fmt.Fprintln(logWriter, "\n--- Processing artifacts ---")

		archive, archiveErr := runner.CreateArtifactsArchive(workspace, jobReq.Job.Artifacts.Paths)
		if archiveErr != nil {
			log.Printf("ERROR: Failed to create artifact archive for job %s: %v", jobReq.ID, archiveErr)
			execErr = archiveErr
		} else if archive.Len() > 0 {
			if uploadErr := a.uploadArtifacts(jobReq.ID, archive); uploadErr != nil {
				log.Printf("ERROR: Failed to upload artifacts for job %s: %v", jobReq.ID, uploadErr)
				execErr = uploadErr
			}
		} else {
			fmt.Fprintln(logWriter, "No files found for artifact paths.")
		}
	}

	// Report the final status
	if execErr != nil {
		log.Printf("Job %s failed: %v", jobReq.ID, execErr)
		a.updateJobStatus(jobReq.ID, shared.JobStatusUpdate{
			Status: shared.StatusFailed,
			Error:  execErr.Error(),
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

// uploadArtifacts handles the multipart file upload.
func (a *Agent) uploadArtifacts(jobID string, archive *bytes.Buffer) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("artifacts", "artifacts.zip")
	if err != nil {
		return err
	}

	if _, err := io.Copy(part, archive); err != nil {
		return err
	}
	writer.Close()

	req, err := http.NewRequest(http.MethodPost, a.serverURL+"/api/artifacts/upload/"+jobID, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	// TODO: Add agent token auth here

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned non-200 status for artifact upload: %s", resp.Status)
	}
	log.Printf("Successfully uploaded artifacts for job %s", jobID)
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

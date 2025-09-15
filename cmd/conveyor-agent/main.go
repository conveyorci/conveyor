package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/conveyorci/conveyor/internal/runner"
	"github.com/conveyorci/conveyor/internal/shared"
)

const serverURL = "http://localhost:8080"

// requestJob polls the server for a new job.
func requestJob() (*shared.JobRequest, error) {
	resp, err := http.Get(serverURL + "/api/jobs/request")
	if err != nil {
		return nil, fmt.Errorf("could not request job: %w", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil // No job available
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

// updateJobStatus sends a status update back to the server.
func updateJobStatus(jobID string, update shared.JobStatusUpdate) error {
	data, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("could not marshal status update: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, serverURL+"/api/jobs/update/"+jobID, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("could not create update request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not send status update: %w", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned non-200 status for update: %s", resp.Status)
	}
	return nil
}

func main() {
	log.Println("Starting Conveyor Agent...")

	dockerExec, err := runner.NewDockerExecutor()
	if err != nil {
		log.Fatalf("Could not create docker executor: %v", err)
	}

	// The main polling loop
	for {
		log.Println("Polling for a new job...")
		jobReq, err := requestJob()
		if err != nil {
			log.Printf("Error requesting job: %v. Retrying in 10s.", err)
			time.Sleep(10 * time.Second)
			continue
		}

		if jobReq == nil {
			// No job available, wait before polling again
			time.Sleep(5 * time.Second)
			continue
		}

		log.Printf("Picked up job %s", jobReq.ID)

		// Update server that we are running the job
		err1 := updateJobStatus(jobReq.ID, shared.JobStatusUpdate{Status: shared.StatusRunning})
		if err1 != nil {
			return
		}

		// Get current directory for workspace mounting
		workspace, err := os.Getwd()
		if err != nil {
			log.Fatalf("Could not get working directory: %v", err)
		}

		// EXECUTE THE JOB
		err = dockerExec.Execute(context.Background(), jobReq.Job, workspace)

		// Report the final status
		if err != nil {
			log.Printf("Job %s failed: %v", jobReq.ID, err)
			err := updateJobStatus(jobReq.ID, shared.JobStatusUpdate{
				Status: shared.StatusFailed,
				Error:  err.Error(),
			})
			if err != nil {
				return
			}
		} else {
			log.Printf("Job %s completed successfully", jobReq.ID)
			err := updateJobStatus(jobReq.ID, shared.JobStatusUpdate{Status: shared.StatusSuccess})
			if err != nil {
				return
			}
		}
	}
}

package shared

import "github.com/conveyorci/conveyor/internal/pipeline"

// JobStatus represents the state of a job.
type JobStatus string

const (
	StatusPending JobStatus = "pending"
	StatusRunning JobStatus = "running"
	StatusSuccess JobStatus = "success"
	StatusFailed  JobStatus = "failed"
)

// JobRequest is the unit of work sent from the server to the agent.
type JobRequest struct {
	ID   string       `json:"id"`
	Job  pipeline.Job `json:"job"`
	Repo string       `json:"repo"` // For later, when we clone repos
}

// JobStatusUpdate is sent from the agent to the server.
type JobStatusUpdate struct {
	Status JobStatus `json:"status"`
	Error  string    `json:"error,omitempty"` // Include error message on failure
}

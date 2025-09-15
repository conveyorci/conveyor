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

// JobRequest is the primary data structure for a job.
// It's used for:
// - Sending work from the server to the agent.
// - Displaying job details in the UI.
// - Storing job state in the database (via the store package).
type JobRequest struct {
	// --- Core Job Information ---
	ID     string       `json:"id"`
	Job    pipeline.Job `json:"job"`  // The actual pipeline definition for this job
	Repo   string       `json:"repo"` // The repository this job belongs to (for future use)
	Status JobStatus    `json:"status,omitempty"`
	Error  string       `json:"error,omitempty"`
}

// JobStatusUpdate is sent from the agent to the server to report progress.
type JobStatusUpdate struct {
	Status JobStatus `json:"status"`
	Error  string    `json:"error,omitempty"`
}

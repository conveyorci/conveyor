package shared

import (
	"time"

	"github.com/conveyorci/conveyor/internal/pipeline"
)

// JobStatus represents the state of a job.
type JobStatus string

const (
	StatusPending JobStatus = "pending"
	StatusRunning JobStatus = "running"
	StatusSuccess JobStatus = "success"
	StatusFailed  JobStatus = "failed"
)

// JobRequest is the primary data structure for a job.
type JobRequest struct {
	ID     string       `json:"id"`
	Job    pipeline.Job `json:"job"`
	Status JobStatus    `json:"status,omitempty"`
	Error  string       `json:"error,omitempty"`

	RepoName      string `json:"repo_name,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	CommitRef     string `json:"commit_ref,omitempty"`
	CommitAuthor  string `json:"commit_author,omitempty"`

	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// JobStatusUpdate is sent from the agent to the server to report progress.
type JobStatusUpdate struct {
	Status JobStatus `json:"status"`
	Error  string    `json:"error,omitempty"`
}

// User represents a user account in the system.
type User struct {
	ID           int       `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	AvatarURL    string    `json:"avatar_url"`
	IsAdmin      bool      `json:"is_admin"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"`
}

// Organization represents a team or group of users.
type Organization struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	OwnerID   int       `json:"owner_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Repository represents a source code repository.
type Repository struct {
	ID       int    `json:"id"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	URL      string `json:"url"`
}

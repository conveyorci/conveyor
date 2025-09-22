package shared

import (
	"database/sql"
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
	ID          int          `json:"id"`
	Owner       string       `json:"owner"`
	Name        string       `json:"name"`
	FullName    string       `json:"full_name"`
	URL         string       `json:"url"`
	LastBuildAt sql.NullTime `json:"last_build_at,omitempty"`
}

// SourceRepo represents a repository from a forge API
type SourceRepo struct {
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	ForgeType   string `json:"forge_type"`
	CloneURL    string `json:"clone_url"`
}

// Connection represents a user's connection to a forge
type Connection struct {
	ID           int       `json:"id"`
	UserID       int       `json:"user_id"`
	ForgeType    string    `json:"forge_type"`
	AccessToken  string    `json:"access_token"` // Be careful not to expose this to the client if not needed
	RefreshToken string    `json:"-"`            // Definitely don't send this to the client
	TokenExpiry  time.Time `json:"token_expiry"`
}

// Artifact represents a file produced by a build job.
type Artifact struct {
	ID          int    `json:"id"`
	JobID       string `json:"job_id"`
	Filename    string `json:"filename"`
	Filesize    int64  `json:"filesize"`
	StoragePath string `json:"-"` // Don't expose the internal disk path in the API
}

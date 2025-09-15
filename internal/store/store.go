package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/conveyorci/conveyor/internal/pipeline"
	"github.com/conveyorci/conveyor/internal/shared"
	_ "github.com/mattn/go-sqlite3" // The underscore imports the driver for side effects
)

// Store manages all database operations.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store and initializes the database schema.
func NewStore(dataSourceName string) (*Store, error) {
	db, err := sql.Open("sqlite3", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("could not open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("could not connect to database: %w", err)
	}

	if err := createSchema(db); err != nil {
		return nil, fmt.Errorf("could not create schema: %w", err)
	}

	log.Println("Database connection established and schema is ready.")
	return &Store{db: db}, nil
}

// createSchema defines and creates the necessary tables.
func createSchema(db *sql.DB) error {
	query := `
    CREATE TABLE IF NOT EXISTS jobs (
        id TEXT PRIMARY KEY,
        status TEXT NOT NULL,
        job_data TEXT NOT NULL, -- Storing the pipeline.Job struct as a JSON blob
        error_message TEXT,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    `
	_, err := db.Exec(query)
	return err
}

// QueueJob inserts a new job into the database with 'pending' status.
func (s *Store) QueueJob(id string, job pipeline.Job) error {
	jobData, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("could not marshal job data: %w", err)
	}

	query := `INSERT INTO jobs (id, status, job_data) VALUES (?, ?, ?)`
	_, err = s.db.Exec(query, id, shared.StatusPending, string(jobData))
	return err
}

// UpdateJobStatus updates the status and error message of a job.
func (s *Store) UpdateJobStatus(id string, status shared.JobStatus, errorMsg string) error {
	query := `UPDATE jobs SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := s.db.Exec(query, status, errorMsg, id)
	return err
}

// RequestJob finds a 'pending' job, updates its status to 'running', and returns it.
// This is done in a transaction to prevent race conditions between agents.
func (s *Store) RequestJob() (*shared.JobRequest, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // Rollback if anything fails

	// Find a pending job, locking the row for update.
	query := `SELECT id, job_data FROM jobs WHERE status = ? ORDER BY created_at ASC LIMIT 1 FOR UPDATE`
	row := tx.QueryRow(query, shared.StatusPending)

	var jobReq shared.JobRequest
	var jobData string
	if err := row.Scan(&jobReq.ID, &jobData); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No pending jobs, not an error
		}
		return nil, err
	}

	// Update the job's status to 'running'
	updateQuery := `UPDATE jobs SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	if _, err := tx.Exec(updateQuery, shared.StatusRunning, jobReq.ID); err != nil {
		return nil, err
	}

	// Unmarshal the job data
	if err := json.Unmarshal([]byte(jobData), &jobReq.Job); err != nil {
		return nil, err
	}

	// If everything succeeded, commit the transaction
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &jobReq, nil
}

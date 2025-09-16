package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/conveyorci/conveyor/internal/pipeline"
	"github.com/conveyorci/conveyor/internal/shared"
	_ "github.com/mattn/go-sqlite3" // The underscore imports the driver for side effects
	"golang.org/x/crypto/bcrypt"
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

	// Enable Write-Ahead Logging for better concurrency.
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		return nil, fmt.Errorf("could not enable WAL mode: %w", err)
	}

	if err := createSchema(db); err != nil {
		return nil, fmt.Errorf("could not create schema: %w", err)
	}

	log.Println("Database connection established and schema is ready.")
	return &Store{db: db}, nil
}

// createSchema defines and creates the necessary tables.
func createSchema(db *sql.DB) error {
	jobsTable := `
    CREATE TABLE IF NOT EXISTS jobs (
        id TEXT PRIMARY KEY,
        status TEXT NOT NULL,
        job_data TEXT NOT NULL, -- Storing the pipeline.Job struct as a JSON blob
        error_message TEXT,
        created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    `
	if _, err := db.Exec(jobsTable); err != nil {
		return err
	}

	usersTable := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := db.Exec(usersTable)
	return err
}

// --- USERS ---

// CreateUser hashes a password and stores a new user in the database.
func (s *Store) CreateUser(username, password string) error {
	// hashing the password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("could not hash password: %w", err)
	}

	query := `INSERT INTO users (username, password_hash) VALUES (?, ?)`
	_, err = s.db.Exec(query, username, string(hashedPassword))
	return err
}

// AuthenticateUser checks if a username and password are valid.
func (s *Store) AuthenticateUser(username, password string) (bool, error) {
	var hashedPassword string
	query := `SELECT password_hash FROM users WHERE username = ?`
	row := s.db.QueryRow(query, username)
	if err := row.Scan(&hashedPassword); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil // User not found
		}
		return false, err
	}

	// Compare the provided password with the stored hash.
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	if err == nil {
		return true, nil // Passwords match
	}
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return false, nil // Passwords do not match
	}
	return false, err
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
	// Start the transaction
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}

	// Find a pending job
	query := `SELECT id, job_data FROM jobs WHERE status = ? ORDER BY created_at ASC LIMIT 1`
	row := tx.QueryRow(query, shared.StatusPending)

	var jobReq shared.JobRequest
	var jobData string
	if err := row.Scan(&jobReq.ID, &jobData); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// This is not an error, it's an empty queue.
			// We must commit the empty transaction to release locks.
			err := tx.Commit()
			if err != nil {
				return nil, err
			}
			return nil, nil
		}
		// Any other scan error is a real problem, so we roll back.
		err := tx.Rollback()
		if err != nil {
			return nil, err
		}
		return nil, err
	}

	// Update the job's status to 'running'
	updateQuery := `UPDATE jobs SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	if _, err := tx.Exec(updateQuery, shared.StatusRunning, jobReq.ID); err != nil {
		err := tx.Rollback()
		if err != nil {
			return nil, err
		}
		return nil, err
	}

	if err := json.Unmarshal([]byte(jobData), &jobReq.Job); err != nil {
		err := tx.Rollback()
		if err != nil {
			return nil, err
		}
		return nil, err
	}

	// If everything succeeded, commit the transaction and return the job.
	// If the commit fails, the transaction is automatically rolled back by the driver.
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &jobReq, nil
}

// ListJobs retrieves all jobs from the database, newest first.
func (s *Store) ListJobs() ([]shared.JobRequest, error) {
	query := `SELECT id, status, job_data, error_message FROM jobs ORDER BY created_at DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []shared.JobRequest
	for rows.Next() {
		var jobReq shared.JobRequest
		var jobData, errorMsg sql.NullString // Use NullString for optional fields
		if err := rows.Scan(&jobReq.ID, &jobReq.Status, &jobData, &errorMsg); err != nil {
			return nil, err
		}
		if jobData.Valid {
			err := json.Unmarshal([]byte(jobData.String), &jobReq.Job)
			if err != nil {
				return nil, err
			}
		}
		if errorMsg.Valid {
			jobReq.Error = errorMsg.String
		}
		jobs = append(jobs, jobReq)
	}
	return jobs, nil
}

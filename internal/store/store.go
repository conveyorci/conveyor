package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/conveyorci/conveyor/internal/pipeline"
	"github.com/conveyorci/conveyor/internal/shared"
	_ "github.com/mattn/go-sqlite3"
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
	// TODO: add an `organization_id` to the `repositories` table.
	repoTable := `
	CREATE TABLE IF NOT EXISTS repositories (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		owner TEXT NOT NULL,
		name TEXT NOT NULL,
		full_name TEXT UNIQUE NOT NULL,
		url TEXT
	);`
	if _, err := db.Exec(repoTable); err != nil {
		return err
	}

	jobsTable := `
	CREATE TABLE IF NOT EXISTS jobs (
		id TEXT PRIMARY KEY,
		repo_id INTEGER,
		status TEXT NOT NULL,
		job_data TEXT NOT NULL,
		error_message TEXT,
		commit_message TEXT,
		commit_sha TEXT,
		commit_ref TEXT,
		commit_author TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		started_at DATETIME,
		finished_at DATETIME,
		FOREIGN KEY(repo_id) REFERENCES repositories(id)
	);`
	if _, err := db.Exec(jobsTable); err != nil {
		return err
	}

	logsTable := `
	CREATE TABLE IF NOT EXISTS logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id TEXT NOT NULL,
		content BLOB,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(job_id) REFERENCES jobs(id)
	);`
	if _, err := db.Exec(logsTable); err != nil {
		return err
	}

	artifactsTable := `
	CREATE TABLE IF NOT EXISTS artifacts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id TEXT NOT NULL,
		filename TEXT NOT NULL,
		filesize INTEGER,
		storage_path TEXT NOT NULL, -- Path on the server's disk
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(job_id) REFERENCES jobs(id)
	);`
	if _, err := db.Exec(artifactsTable); err != nil {
		return err
	}

	usersTable := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		email TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		avatar_url TEXT, -- URL to their profile picture
		is_admin BOOLEAN NOT NULL DEFAULT FALSE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := db.Exec(usersTable); err != nil {
		return err
	}

	orgsTable := `
	CREATE TABLE IF NOT EXISTS organizations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		owner_id INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(owner_id) REFERENCES users(id)
	);`
	if _, err := db.Exec(orgsTable); err != nil {
		return err
	}

	orgMembersTable := `
	CREATE TABLE IF NOT EXISTS organization_members (
		user_id INTEGER NOT NULL,
		organization_id INTEGER NOT NULL,
		role TEXT NOT NULL, -- e.g., 'owner', 'member'
		PRIMARY KEY(user_id, organization_id),
		FOREIGN KEY(user_id) REFERENCES users(id),
		FOREIGN KEY(organization_id) REFERENCES organizations(id)
	);`
	if _, err := db.Exec(orgMembersTable); err != nil {
		return err
	}

	// --- FORGE/SCM CONNECTIONS (e.g., GitHub) ---
	connectionsTable := `
	CREATE TABLE IF NOT EXISTS connections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		forge_type TEXT NOT NULL, -- 'github', 'gitlab', etc.
		access_token TEXT NOT NULL, -- This should be encrypted!
		refresh_token TEXT,
		token_expiry DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(user_id) REFERENCES users(id)
	);`
	_, err := db.Exec(connectionsTable)

	return err
}

// CreateUser hashes a password and stores a new user in the database.
func (s *Store) CreateUser(username, password, email string) error {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("could not hash password: %w", err)
	}

	query := `INSERT INTO users (username, password_hash, email) VALUES (?, ?, ?)`
	_, err = s.db.Exec(query, username, string(hashedPassword), email)
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

// GetUserByUsername retrieves a user's details from the database.
func (s *Store) GetUserByUsername(username string) (*shared.User, error) {
	var user shared.User
	query := `SELECT id, username, email, password_hash, is_admin, created_at FROM users WHERE username = ?`
	row := s.db.QueryRow(query, username)
	if err := row.Scan(&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.IsAdmin, &user.CreatedAt); err != nil {
		return nil, err
	}
	return &user, nil
}

// IsUserAdmin checks if a user has admin privileges.
func (s *Store) IsUserAdmin(username string) (bool, error) {
	var isAdmin bool
	query := `SELECT is_admin FROM users WHERE username = ?`
	err := s.db.QueryRow(query, username).Scan(&isAdmin)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil // User not found, so not an admin
		}
		return false, err
	}
	return isAdmin, nil
}

// ListUsers retrieves all users from the database for the admin panel.
func (s *Store) ListUsers() ([]shared.User, error) {
	query := `SELECT id, username, email, is_admin, created_at FROM users ORDER BY username ASC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []shared.User
	for rows.Next() {
		var user shared.User
		if err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.IsAdmin, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, nil
}

// CreateOrganization creates a new organization and adds the owner as a member.
func (s *Store) CreateOrganization(name string, ownerID int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	res, err := tx.Exec(`INSERT INTO organizations (name, owner_id) VALUES (?, ?)`, name, ownerID)
	if err != nil {
		tx.Rollback()
		return err
	}
	orgID, err := res.LastInsertId()
	if err != nil {
		tx.Rollback()
		return err
	}

	_, err = tx.Exec(`INSERT INTO organization_members (user_id, organization_id, role) VALUES (?, ?, ?)`, ownerID, orgID, "owner")
	if err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// ListOrgsForUser retrieves all organizations a user is a member of.
func (s *Store) ListOrgsForUser(userID int) ([]shared.Organization, error) {
	query := `
		SELECT o.id, o.name, o.owner_id, o.created_at 
		FROM organizations o
		JOIN organization_members om ON o.id = om.organization_id
		WHERE om.user_id = ?
		ORDER BY o.name ASC
	`
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []shared.Organization
	for rows.Next() {
		var org shared.Organization
		if err := rows.Scan(&org.ID, &org.Name, &org.OwnerID, &org.CreatedAt); err != nil {
			return nil, err
		}
		orgs = append(orgs, org)
	}
	return orgs, nil
}

// ListRepositories retrieves a unique list of repositories that have had builds.
func (s *Store) ListRepositories() ([]shared.Repository, error) {
	// TODO: improve this later
	query := `SELECT DISTINCT repo_name FROM jobs WHERE repo_name IS NOT NULL ORDER BY repo_name ASC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []shared.Repository
	for rows.Next() {
		var repo shared.Repository
		if err := rows.Scan(&repo.FullName); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

// GetPipelineByID retrieves a single job's full details by its UUID.
func (s *Store) GetPipelineByID(id string) (*shared.JobRequest, error) {
	query := `
		SELECT 
			id, status, job_data, error_message, repo_name, 
			commit_message, commit_sha, commit_ref, commit_author, 
			created_at, started_at, finished_at 
		FROM jobs 
		WHERE id = ?
	`
	row := s.db.QueryRow(query, id)

	var jobReq shared.JobRequest
	var jobData, errorMsg, repoName, commitMsg, commitSha, commitRef, commitAuthor sql.NullString
	var createdAt, startedAt, finishedAt sql.NullTime

	err := row.Scan(
		&jobReq.ID, &jobReq.Status, &jobData, &errorMsg, &repoName,
		&commitMsg, &commitSha, &commitRef, &commitAuthor,
		&createdAt, &startedAt, &finishedAt,
	)
	if err != nil {
		return nil, err
	}

	if jobData.Valid {
		if err := json.Unmarshal([]byte(jobData.String), &jobReq.Job); err != nil {
			return nil, fmt.Errorf("could not unmarshal job_data for job %s: %w", id, err)
		}
	}

	if errorMsg.Valid {
		jobReq.Error = errorMsg.String
	}
	if repoName.Valid {
		jobReq.RepoName = repoName.String
	}
	if commitMsg.Valid {
		jobReq.CommitMessage = commitMsg.String
	}
	if commitSha.Valid {
		jobReq.CommitSHA = commitSha.String
	}
	if commitRef.Valid {
		jobReq.CommitRef = commitRef.String
	}
	if commitAuthor.Valid {
		jobReq.CommitAuthor = commitAuthor.String
	}

	if createdAt.Valid {
		jobReq.CreatedAt = createdAt.Time
	}
	if startedAt.Valid {
		jobReq.StartedAt = startedAt.Time
	}
	if finishedAt.Valid {
		jobReq.FinishedAt = finishedAt.Time
	}

	return &jobReq, nil
}

// GetLogsForJob retrieves the log content for a specific job.
func (s *Store) GetLogsForJob(jobID string) (string, error) {
	var content string
	query := `SELECT content FROM logs WHERE job_id = ? ORDER BY created_at ASC`
	err := s.db.QueryRow(query, jobID).Scan(&content)
	if err == sql.ErrNoRows {
		return "No logs found for this job.", nil
	}
	return content, err
}

// GetLatestStatusForBranch retrieves the status of the most recent build for a specific branch.
func (s *Store) GetLatestStatusForBranch(repoFullName, branchName string) (shared.JobStatus, error) {
	ref := "refs/heads/" + branchName
	var status shared.JobStatus
	query := `SELECT status FROM jobs WHERE repo_name = ? AND commit_ref = ? ORDER BY created_at DESC LIMIT 1`
	err := s.db.QueryRow(query, repoFullName, ref).Scan(&status)
	return status, err
}

// QueueJob inserts a new job into the database with 'pending' status.
func (s *Store) QueueJob(id string, job pipeline.Job, repo, msg, sha, ref, author string) error {
	jobData, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("could not marshal job data: %w", err)
	}

	query := `INSERT INTO jobs (id, status, job_data, repo_name, commit_message, commit_sha, commit_ref, commit_author) 
              VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.Exec(query, id, shared.StatusPending, string(jobData), repo, msg, sha, ref, author)
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
	query := `SELECT id, status, job_data, error_message, repo_name, commit_message, commit_sha, commit_ref, commit_author, created_at, started_at, finished_at 
              FROM jobs ORDER BY created_at DESC`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []shared.JobRequest
	for rows.Next() {
		var jobReq shared.JobRequest
		var jobData, errorMsg, repo, msg, sha, ref, author sql.NullString
		var createdAt, startedAt, finishedAt sql.NullTime

		if err := rows.Scan(&jobReq.ID, &jobReq.Status, &jobData, &errorMsg, &repo, &msg, &sha, &ref, &author, &createdAt, &startedAt, &finishedAt); err != nil {
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

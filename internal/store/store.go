package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

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
		repo_id INTEGER NOT NULL,
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

	// why tf is there --- shit, i deleted them
	// FORGE/SCM CONNECTIONS (e.g., GitHub)
	connectionsTable := `
	CREATE TABLE IF NOT EXISTS connections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		forge_type TEXT NOT NULL, -- 'github', 'gitlab', etc.
		access_token TEXT NOT NULL, -- This should be encrypted!
		refresh_token TEXT,
		token_expiry DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(user_id) REFERENCES users(id),
	    UNIQUE(user_id, forge_type)
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

// ActivateRepository adds a repository to the local database.
func (s *Store) ActivateRepository(fullName, cloneURL, owner, name string) (*shared.Repository, error) {
	query := `INSERT INTO repositories (full_name, url, owner, name) VALUES (?, ?, ?, ?)`
	res, err := s.db.Exec(query, fullName, cloneURL, owner, name)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &shared.Repository{
		ID:       int(id),
		FullName: fullName,
		URL:      cloneURL,
		Owner:    owner,
		Name:     name,
	}, nil
}

// ListRepositories now reads from the dedicated repositories table.
func (s *Store) ListRepositories() ([]shared.Repository, error) {
	query := `
		SELECT 
			r.id, 
			r.owner, 
			r.name, 
			r.full_name, 
			r.url, 
			MAX(j.created_at) as last_build_at
		FROM 
			repositories r
		LEFT JOIN 
			jobs j ON r.id = j.repo_id
		GROUP BY 
			r.id
		ORDER BY 
			last_build_at DESC NULLS LAST, r.full_name ASC;
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query repositories: %w", err)
	}
	defer rows.Close()

	var repos []shared.Repository
	for rows.Next() {
		var repo shared.Repository
		if err := rows.Scan(&repo.ID, &repo.Owner, &repo.Name, &repo.FullName, &repo.URL, &repo.LastBuildAt); err != nil {
			return nil, fmt.Errorf("failed to scan repository row: %w", err)
		}
		repos = append(repos, repo)
	}

	// Check for any errors during iteration
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating repository rows: %w", err)
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
func (s *Store) QueueJob(id string, job pipeline.Job, repoFullName, msg, sha, ref, author string) error {
	jobData, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("could not marshal job data: %w", err)
	}

	var repoID int
	err = s.db.QueryRow("SELECT id FROM repositories WHERE full_name = ?", repoFullName).Scan(&repoID)
	if err != nil {
		// this can happen if a webhook fires for a repo that hasn't been activated yet
		// we could choose to auto-activate it here, or just log an error
		// i need to think about this
		return fmt.Errorf("could not find activated repository with name '%s': %w", repoFullName, err)
	}
	// ---

	query := `
		INSERT INTO jobs (id, repo_id, status, job_data, commit_message, commit_sha, commit_ref, commit_author) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = s.db.Exec(query, id, repoID, shared.StatusPending, string(jobData), msg, sha, ref, author)
	return err
}

// UpdateJobStatus updates the status and error message of a job.
func (s *Store) UpdateJobStatus(id string, status shared.JobStatus, errorMsg string) error {
	query := `UPDATE jobs SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := s.db.Exec(query, status, errorMsg, id)
	return err
}

// SaveUserConnection saves or updates a user's OAuth token for a specific forge.
// It checks if a connection already exists and performs an INSERT or UPDATE accordingly.
func (s *Store) SaveUserConnection(userID int, forgeType, accessToken, refreshToken string, expiry time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("could not begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Check if a connection already exist
	var exists int
	err = tx.QueryRow("SELECT 1 FROM connections WHERE user_id = ? AND forge_type = ?", userID, forgeType).Scan(&exists)

	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("could not check for existing connection: %w", err)
	}

	if err == sql.ErrNoRows {
		// Connection does not exist
		_, err = tx.Exec(
			"INSERT INTO connections (user_id, forge_type, access_token, refresh_token, token_expiry) VALUES (?, ?, ?, ?, ?)",
			userID, forgeType, accessToken, refreshToken, expiry,
		)
	} else {
		// Connection exists
		_, err = tx.Exec(
			"UPDATE connections SET access_token = ?, refresh_token = ?, token_expiry = ? WHERE user_id = ? AND forge_type = ?",
			accessToken, refreshToken, expiry, userID, forgeType,
		)
	}

	if err != nil {
		return fmt.Errorf("could not save connection: %w", err)
	}

	return tx.Commit()
}

// ListUserConnections retrieves all forge connections for a given user.
func (s *Store) ListUserConnections(userID int) ([]shared.Connection, error) {
	query := `SELECT id, user_id, forge_type, access_token, token_expiry FROM connections WHERE user_id = ?`
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var connections []shared.Connection
	for rows.Next() {
		var conn shared.Connection
		// DOOO NOT select refresh_token to avoid exposing it unnecessarily
		if err := rows.Scan(&conn.ID, &conn.UserID, &conn.ForgeType, &conn.AccessToken, &conn.TokenExpiry); err != nil {
			return nil, err
		}
		connections = append(connections, conn)
	}
	return connections, nil
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

// ListJobs retrieves jobs from the database, newest first.
// If repoFilter (e.g., "user/repo") is not empty, it will only return jobs for that repository.
func (s *Store) ListJobs(repoFilter string) ([]shared.JobRequest, error) {
	query := `
		SELECT 
			j.id, j.status, j.job_data, j.error_message, r.full_name, 
			j.commit_message, j.commit_sha, j.commit_ref, j.commit_author, 
			j.created_at, j.started_at, j.finished_at 
		FROM 
			jobs j
		LEFT JOIN 
			repositories r ON j.repo_id = r.id
	`
	args := []interface{}{}

	if repoFilter != "" {
		query += " WHERE r.full_name = ?"
		args = append(args, repoFilter)
	}

	query += " ORDER BY j.created_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	defer rows.Close()

	var jobs []shared.JobRequest
	for rows.Next() {
		var jobReq shared.JobRequest
		var jobData, errorMsg, repoName, commitMsg, commitSha, commitRef, commitAuthor sql.NullString
		var createdAt, startedAt, finishedAt sql.NullTime

		err := rows.Scan(
			&jobReq.ID, &jobReq.Status, &jobData, &errorMsg, &repoName,
			&commitMsg, &commitSha, &commitRef, &commitAuthor,
			&createdAt, &startedAt, &finishedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan job row: %w", err)
		}

		// Process and assign the nullable fields
		if jobData.Valid {
			if err := json.Unmarshal([]byte(jobData.String), &jobReq.Job); err != nil {
				return nil, fmt.Errorf("could not unmarshal job_data: %w", err)
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

		jobs = append(jobs, jobReq)
	}

	// Check for any errors
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error during job row iteration: %w", err)
	}

	return jobs, nil
}

// CreateArtifact records a new artifact in the database.
func (s *Store) CreateArtifact(jobID, filename string, filesize int64, storagePath string) error {
	query := `INSERT INTO artifacts (job_id, filename, filesize, storage_path) VALUES (?, ?, ?, ?)`
	_, err := s.db.Exec(query, jobID, filename, filesize, storagePath)
	if err != nil {
		return fmt.Errorf("could not insert artifact record: %w", err)
	}
	return nil
}

// ListArtifactsForJob retrieves all artifacts associated with a specific job.
func (s *Store) ListArtifactsForJob(jobID string) ([]shared.Artifact, error) {
	query := `SELECT id, job_id, filename, filesize FROM artifacts WHERE job_id = ?`
	rows, err := s.db.Query(query, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []shared.Artifact
	for rows.Next() {
		var art shared.Artifact
		if err := rows.Scan(&art.ID, &art.JobID, &art.Filename, &art.Filesize); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, art)
	}
	return artifacts, nil
}

// GetArtifactByID retrieves a single artifact's details, including its storage path for download.
func (s *Store) GetArtifactByID(artifactID int) (*shared.Artifact, error) {
	var art shared.Artifact
	query := `SELECT id, job_id, filename, filesize, storage_path FROM artifacts WHERE id = ?`
	row := s.db.QueryRow(query, artifactID)
	err := row.Scan(&art.ID, &art.JobID, &art.Filename, &art.Filesize, &art.StoragePath)
	if err != nil {
		return nil, err
	}
	return &art, nil
}

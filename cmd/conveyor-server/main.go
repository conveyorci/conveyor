package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/go-github/v74/github"
	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"gopkg.in/yaml.v3"

	"github.com/conveyorci/conveyor/internal/config"
	"github.com/conveyorci/conveyor/internal/pipeline"
	"github.com/conveyorci/conveyor/internal/serverui"
	"github.com/conveyorci/conveyor/internal/shared"
	"github.com/conveyorci/conveyor/internal/store"
)

// Server holds dependencies like the database store.
type Server struct {
	store       *store.Store
	cookieStore *sessions.CookieStore
	config      *config.Config
}

// NewServer creates a new server instance.
func NewServer(store *store.Store, cfg *config.Config) *Server {
	authKey := []byte(cfg.Security.SessionKey)
	cookieStore := sessions.NewCookieStore(authKey)
	return &Server{store: store, cookieStore: cookieStore, config: cfg}
}

// getUsernameFromSession gets username from session.
func (s *Server) getUsernameFromSession(r *http.Request) (string, error) {
	session, err := s.cookieStore.Get(r, "conveyor-session")
	if err != nil {
		return "", err
	}
	username, ok := session.Values["username"].(string)
	if !ok || username == "" {
		return "", fmt.Errorf("user not authenticated")
	}
	return username, nil
}

// TODO: fix that all auth things after web UI will be done
func (s *Server) registrationHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")
	email := r.FormValue("email")

	if err := s.store.CreateUser(username, password, email); err != nil {
		http.Error(w, "Registration failed, username or email may already be taken.", http.StatusInternalServerError)
		log.Printf("ERROR: failed to create user: %v", err)
		return
	}
	http.Redirect(w, r, "/login.html", http.StatusFound)
}

func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")

	ok, err := s.store.AuthenticateUser(username, password)
	if err != nil || !ok {
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}

	session, _ := s.cookieStore.Get(r, "conveyor-session")
	session.Values["authenticated"] = true
	session.Values["username"] = username
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) logoutHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := s.cookieStore.Get(r, "conveyor-session")
	session.Values["authenticated"] = false
	session.Options.MaxAge = -1
	session.Save(r, w)
	http.Redirect(w, r, "/login.html", http.StatusFound)
}

// TODO: fix that all auth things after Vue UI will be done
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _ := s.cookieStore.Get(r, "conveyor-session")
		if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) adminOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, err := s.getUsernameFromSession(r)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		isAdmin, err := s.store.IsUserAdmin(username)
		if err != nil {
			http.Error(w, "Could not verify user permissions", http.StatusInternalServerError)
			return
		}
		if !isAdmin {
			http.Error(w, "Forbidden: Administrator access required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// /api/repositories
func (s *Server) listReposHandler(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepositories()
	if err != nil {
		http.Error(w, "Failed to retrieve repositories", http.StatusInternalServerError)
		log.Printf("ERROR: listReposHandler: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(repos)
}

// /api/pipelines/{id}
func (s *Server) pipelineDetailHandler(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimPrefix(r.URL.Path, "/api/pipelines/")
	job, err := s.store.GetPipelineByID(jobID)
	if err != nil {
		http.Error(w, "Pipeline not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

// /api/logs/{job_id}
func (s *Server) logsHandler(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	logs, err := s.store.GetLogsForJob(jobID)
	if err != nil {
		http.Error(w, "Logs not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(logs))
}

// /api/badges/{owner}/{repo}?branch=main
func (s *Server) badgeHandler(w http.ResponseWriter, r *http.Request) {
	// example path: /api/badges/conveyorci/conveyor
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/badges/"), "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid repo path", http.StatusBadRequest)
		return
	}
	repoFullName := fmt.Sprintf("%s/%s", parts[0], parts[1])
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main" // default branch
	}

	status, err := s.store.GetLatestStatusForBranch(repoFullName, branch)
	if err != nil {
		status = "unknown"
	}

	// simple SVG generation
	color := "lightgrey"
	switch status {
	case shared.StatusSuccess:
		color = "green"
	case shared.StatusFailed:
		color = "red"
	case shared.StatusRunning:
		color = "blue"
	}

	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="90" height="20"><linearGradient id="b" x2="0" y2="100%%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient><mask id="a"><rect width="90" height="20" rx="3" fill="#fff"/></mask><g mask="url(#a)"><path fill="#555" d="M0 0h37v20H0z"/><path fill="%s" d="M37 0h53v20H37z"/><path fill="url(#b)" d="M0 0h90v20H0z"/></g><g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,sans-serif" font-size="11"><text x="18.5" y="15" fill="#010101" fill-opacity=".3">build</text><text x="18.5" y="14">build</text><text x="62.5" y="15" fill="#010101" fill-opacity=".3">%s</text><text x="62.5" y="14">%s</text></g></svg>`, color, status, status)

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write([]byte(svg))
}

// /api/user/profile
func (s *Server) userProfileHandler(w http.ResponseWriter, r *http.Request) {
	username, err := s.getUsernameFromSession(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// do not send the password hash to the client
	user.PasswordHash = ""

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// /api/orgs
func (s *Server) createOrgHandler(w http.ResponseWriter, r *http.Request) {
	username, err := s.getUsernameFromSession(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	var reqBody struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := s.store.CreateOrganization(reqBody.Name, user.ID); err != nil {
		http.Error(w, "Failed to create organization", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// /api/user/orgs
func (s *Server) listUserOrgsHandler(w http.ResponseWriter, r *http.Request) {
	username, err := s.getUsernameFromSession(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	orgs, err := s.store.ListOrgsForUser(user.ID)
	if err != nil {
		http.Error(w, "Failed to retrieve organizations", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(orgs)
}

// /api/admin/users
func (s *Server) adminListUsersHandler(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		http.Error(w, "Failed to retrieve users", http.StatusInternalServerError)
		return
	}

	for i := range users {
		users[i].PasswordHash = ""
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

func (s *Server) requestJobHandler(w http.ResponseWriter, r *http.Request) {
	jobReq, err := s.store.RequestJob()
	if err != nil {
		http.Error(w, "Failed to request job from store", http.StatusInternalServerError)
		log.Printf("ERROR: requestJobHandler: %v", err)
		return
	}

	if jobReq == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	log.Printf("Dispatching job %s to an agent", jobReq.ID)
	w.Header().Set("Content-Type", "application/json")
	err1 := json.NewEncoder(w).Encode(jobReq)
	if err1 != nil {
		return
	}
}

func (s *Server) updateJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Path[len("/api/jobs/update/"):]
	var update shared.JobStatusUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.store.UpdateJobStatus(jobID, update.Status, update.Error); err != nil {
		http.Error(w, "Failed to update job status", http.StatusInternalServerError)
		log.Printf("ERROR: updateJobHandler: %v", err)
		return
	}

	log.Printf("Received status update for job %s: %s", jobID, update.Status)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) listJobsHandler(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs()
	if err != nil {
		http.Error(w, "Failed to retrieve jobs", http.StatusInternalServerError)
		log.Printf("ERROR: listJobsHandler: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	err1 := json.NewEncoder(w).Encode(jobs)
	if err1 != nil {
		return
	}
}

func (s *Server) githubWebhookHandler(w http.ResponseWriter, r *http.Request) {
	// TODO: add check if it is project connected to Git origin
	secret := s.config.Security.GitHubWebhookSecret
	if secret == "" {
		log.Println("WARN: GITHUB_WEBHOOK_SECRET is not set in the config file.")
		http.Error(w, "Webhook secret not configured", http.StatusInternalServerError)
		return
	}

	payload, err := github.ValidatePayload(r, []byte(secret))
	if err != nil {
		log.Printf("WARN: Invalid webhook payload: %v", err)
		http.Error(w, "Invalid payload", http.StatusUnauthorized)
		return
	}

	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		log.Printf("WARN: Could not parse webhook: %v", err)
		http.Error(w, "Could not parse webhook", http.StatusBadRequest)
		return
	}

	pushEvent, ok := event.(*github.PushEvent)
	if !ok {
		log.Println("Received webhook, but it was not a push event.")
		fmt.Fprint(w, "Event was not a push event, ignoring.")
		return
	}

	repoURL := *pushEvent.Repo.CloneURL
	log.Printf("Received push event for repo: %s", repoURL)

	tempDir, err := os.MkdirTemp("", "conveyor-workspace-*")
	if err != nil {
		log.Printf("ERROR: Failed to create temp workspace: %v", err)
		http.Error(w, "Failed to create temp workspace", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tempDir)
	log.Printf("Created temporary workspace: %s", tempDir)

	// debugging
	log.Printf("Cloning repository %s into %s", repoURL, tempDir)
	cmd := exec.Command("git", "clone", repoURL, tempDir)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("ERROR: Failed to clone repository. Exit error: %v", err)
		log.Printf("Git command output:\n%s", string(output))
		http.Error(w, "Failed to clone repository", http.StatusInternalServerError)
		return
	}
	log.Println("Successfully cloned repository.")

	conveyorFile := filepath.Join(tempDir, ".conveyor.yml")
	data, err := os.ReadFile(conveyorFile)
	if err != nil {
		log.Printf("ERROR: Could not read .conveyor.yml from repository: %v", err)
		http.Error(w, "Could not read .conveyor.yml from repository", http.StatusBadRequest)
		return
	}
	log.Println("Successfully read .conveyor.yml.")

	var p pipeline.Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		log.Printf("ERROR: Could not parse .conveyor.yml: %v", err)
		http.Error(w, "Could not parse .conveyor.yml", http.StatusBadRequest)
		return
	}

	log.Printf("Parsed .conveyor.yml and found %d jobs.", len(p.Jobs))

	if len(p.Jobs) == 0 {
		log.Println("WARN: No jobs found in .conveyor.yml, nothing to queue.")
	}

	repoName := *pushEvent.Repo.FullName
	headCommit := pushEvent.HeadCommit
	commitMessage := *headCommit.Message
	commitSHA := *headCommit.ID
	commitRef := *pushEvent.Ref
	commitAuthor := *headCommit.Author.Name

	for jobName, job := range p.Jobs {
		jobID := uuid.New().String()
		if err := s.store.QueueJob(jobID, job, repoName, commitMessage, commitSHA, commitRef, commitAuthor); err != nil {
			log.Printf("ERROR: Failed to queue job '%s': %v", jobName, err)
			continue
		}
		log.Printf("Queued job '%s' with ID %s from webhook", jobName, jobID)
	}

	fmt.Fprintf(w, "Webhook processed. Queued %d jobs.\n", len(p.Jobs))
}

func main() {
	configPath := flag.String("config", "config.yml", "Path to the configuration file")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("FATAL: Failed to load configuration: %v", err)
	}
	log.Printf("Configuration loaded from %s", *configPath)

	db, err := store.NewStore(cfg.Server.Database)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize database: %v", err)
	}

	server := NewServer(db, cfg)
	mux := http.NewServeMux()

	mux.HandleFunc("/api/register", server.registrationHandler)
	mux.HandleFunc("/api/login", server.loginHandler)
	mux.HandleFunc("/api/logout", server.logoutHandler)
	mux.HandleFunc("/api/badges/", server.badgeHandler)

	uiHandler := serverui.New()
	mux.Handle("/login.html", uiHandler)
	mux.Handle("/register.html", uiHandler)

	mux.HandleFunc("/api/jobs/request", server.requestJobHandler)
	mux.HandleFunc("/api/jobs/update/", server.updateJobHandler)
	mux.HandleFunc("/webhooks/github", server.githubWebhookHandler)

	mux.Handle("/api/jobs", server.authMiddleware(http.HandlerFunc(server.listJobsHandler)))
	mux.Handle("/api/repositories", server.authMiddleware(http.HandlerFunc(server.listReposHandler)))
	mux.Handle("/api/pipelines/", server.authMiddleware(http.HandlerFunc(server.pipelineDetailHandler)))
	mux.Handle("/api/logs/", server.authMiddleware(http.HandlerFunc(server.logsHandler)))
	mux.Handle("/api/user/profile", server.authMiddleware(http.HandlerFunc(server.userProfileHandler)))
	mux.Handle("/api/user/orgs", server.authMiddleware(http.HandlerFunc(server.listUserOrgsHandler)))
	mux.Handle("/api/orgs", server.authMiddleware(http.HandlerFunc(server.createOrgHandler)))

	mux.Handle("/api/admin/users", server.authMiddleware(server.adminOnlyMiddleware(http.HandlerFunc(server.adminListUsersHandler))))

	mux.Handle("/", server.authMiddleware(uiHandler))

	port := cfg.Server.Port
	log.Printf("Starting Conveyor Server on port %s", port)
	log.Printf("Access the UI at http://localhost:%s", port)

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

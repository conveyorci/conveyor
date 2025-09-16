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

// --- HTTP Handlers ---

// TODO: fix that all auth things after Vue UI will be done
//func (s *Server) registrationHandler(w http.ResponseWriter, r *http.Request) {
//	r.ParseForm()
//	username := r.FormValue("username")
//	password := r.FormValue("password")
//
//	if err := s.store.CreateUser(username, password); err != nil {
//		http.Error(w, "Registration failed", http.StatusInternalServerError)
//		log.Printf("ERROR: failed to create user: %v", err)
//		return
//	}
//	// Redirect to login page on successful registration
//	http.Redirect(w, r, "/login.html", http.StatusFound)
//}

func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	username := r.FormValue("username")
	password := r.FormValue("password")

	ok, err := s.store.AuthenticateUser(username, password)
	if err != nil || !ok {
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}

	// Create a new session
	session, _ := s.cookieStore.Get(r, "conveyor-session")
	session.Values["authenticated"] = true
	session.Values["username"] = username
	session.Save(r, w)

	// Redirect to the dashboard on successful login
	http.Redirect(w, r, "/", http.StatusFound)
}

// TODO: fix that all auth things after Vue UI will be done
//func (s *Server) logoutHandler(w http.ResponseWriter, r *http.Request) {
//	session, _ := s.cookieStore.Get(r, "conveyor-session")
//	session.Values["authenticated"] = false
//	session.Options.MaxAge = -1 // Delete the cookie
//	session.Save(r, w)
//	http.Redirect(w, r, "/login.html", http.StatusFound)
//}

// TODO: fix that all auth things after Vue UI will be done
//func (s *Server) authMiddleware(next http.Handler) http.Handler {
//	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//		session, _ := s.cookieStore.Get(r, "conveyor-session")
//
//		// Check if user is authenticated
//		if auth, ok := session.Values["authenticated"].(bool); !ok || !auth {
//			// If not authenticated, redirect to the login page
//			http.Redirect(w, r, "/login.html", http.StatusFound)
//			return
//		}
//
//		// If authenticated, call the next handler in the chain
//		next.ServeHTTP(w, r)
//	})
//}

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

	// Parse and queue the jobs
	var p pipeline.Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		log.Printf("ERROR: Could not parse .conveyor.yml: %v", err)
		http.Error(w, "Could not parse .conveyor.yml", http.StatusBadRequest)
		return
	}

	// Log how many jobs were found after parsing
	log.Printf("Parsed .conveyor.yml and found %d jobs.", len(p.Jobs))

	if len(p.Jobs) == 0 {
		log.Println("WARN: No jobs found in .conveyor.yml, nothing to queue.")
	}

	for jobName, job := range p.Jobs {
		jobID := uuid.New().String()
		if err := s.store.QueueJob(jobID, job); err != nil {
			log.Printf("ERROR: Failed to queue job '%s': %v", jobName, err)
			continue
		}
		log.Printf("Queued job '%s' with ID %s from webhook", jobName, jobID)
	}

	fmt.Fprintf(w, "Webhook processed. Queued %d jobs.\n", len(p.Jobs))
}

// --- Main Function ---

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

	// TODO: fix that all auth things after Vue UI will be done
	// mux.HandleFunc("/api/register", server.registrationHandler)
	mux.HandleFunc("/api/login", server.loginHandler)
	// mux.HandleFunc("/api/logout", server.logoutHandler)

	uiHandler := serverui.New()
	mux.Handle("/login.html", uiHandler)
	mux.Handle("/register.html", uiHandler)

	apiJobsHandler := http.HandlerFunc(server.listJobsHandler)
	mux.Handle("/api/jobs", apiJobsHandler)

	// TODO: add token auth for jobs api endpoints
	mux.HandleFunc("/api/jobs/request", server.requestJobHandler)
	mux.HandleFunc("/api/jobs/update/", server.updateJobHandler)
	mux.HandleFunc("/webhooks/github", server.githubWebhookHandler)

	mux.Handle("/", uiHandler)

	// TODO: think about config for Docker's Conveyor image/container
	port := cfg.Server.Port
	log.Printf("Starting Conveyor Server on port %s", port)
	log.Println("Access the UI at http://localhost:8080")

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

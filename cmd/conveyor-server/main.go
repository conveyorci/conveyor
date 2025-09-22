package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/conveyorci/conveyor/internal/serverui"
	"github.com/google/go-github/v74/github"
	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/patrickmn/go-cache"
	"golang.org/x/oauth2"
	ghoauth "golang.org/x/oauth2/github"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"

	"github.com/conveyorci/conveyor/internal/config"
	"github.com/conveyorci/conveyor/internal/pipeline"
	"github.com/conveyorci/conveyor/internal/shared"
	"github.com/conveyorci/conveyor/internal/store"
)

var (
	usernameRegex = regexp.MustCompile("^[a-zA-Z0-9_-]{3,20}$")
	emailRegex    = regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")
)

// Server holds dependencies like the database store.
type Server struct {
	store        *store.Store
	cookieStore  *sessions.CookieStore
	config       *config.Config
	oauthConfigs map[string]*oauth2.Config // map[forge_type]*oauth2.Config
}

// NewServer creates a new server instance.
func NewServer(store *store.Store, cfg *config.Config) *Server {
	authKey := []byte(cfg.Security.SessionKey)
	cookieStore := sessions.NewCookieStore(authKey)
	cookieStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
		Secure:   false, // TODO: set later to true for using HTTPS by default, or no
	}

	// Initialize OAuth configurations based on the config file
	oauthConfigs := make(map[string]*oauth2.Config)
	if cfg.Forges.GitHub.Enabled && cfg.Forges.GitHub.ClientID != "" {
		log.Println("GitHub forge is enabled.")
		oauthConfigs["github"] = &oauth2.Config{
			ClientID:     cfg.Forges.GitHub.ClientID,
			ClientSecret: cfg.Forges.GitHub.ClientSecret,
			Scopes:       []string{"repo", "user:email", "read:org"},
			Endpoint:     ghoauth.Endpoint,
		}
	}

	return &Server{store: store, cookieStore: cookieStore, config: cfg, oauthConfigs: oauthConfigs}
}

// IPRateLimiter holds a rate limiter for each IP address.
type IPRateLimiter struct {
	ips *cache.Cache
	mu  sync.Mutex
}

// NewIPRateLimiter creates a new rate limiter.
func NewIPRateLimiter() *IPRateLimiter {
	return &IPRateLimiter{
		// Create a cache that cleans up expired entries every 10 minutes.
		ips: cache.New(10*time.Minute, 10*time.Minute),
	}
}

// getLimiter returns the rate limiter for a given IP address.
func (i *IPRateLimiter) getLimiter(ip string) *rate.Limiter {
	i.mu.Lock()
	defer i.mu.Unlock()

	limiter, found := i.ips.Get(ip)
	if !found {
		// 5 events per minute
		limiter = rate.NewLimiter(rate.Every(1*time.Minute), 5)
		i.ips.Set(ip, limiter, cache.DefaultExpiration)
	}
	return limiter.(*rate.Limiter)
}

// rateLimitMiddleware is a middleware that applies rate limiting.
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	limiter := NewIPRateLimiter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			log.Printf("could not get ip: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if !limiter.getLimiter(ip).Allow() {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// getUsernameFromSession gets username from session.
func (s *Server) getUsernameFromSession(r *http.Request) (string, error) {
	session, err := s.cookieStore.Get(r, "conveyor-session")
	if err != nil {
		return "", fmt.Errorf("could not get session: %w", err)
	}
	username, ok := session.Values["username"].(string)
	if !ok || username == "" {
		return "", errors.New("user not authenticated")
	}
	return username, nil
}

// TODO: fix that all auth things after web UI will be done
func (s *Server) registrationHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	email := r.FormValue("email")

	if !usernameRegex.MatchString(username) {
		http.Error(w, "Invalid username format.", http.StatusBadRequest)
		return
	}
	if !emailRegex.MatchString(email) {
		http.Error(w, "Invalid email format.", http.StatusBadRequest)
		return
	}
	if len(password) < 6 {
		http.Error(w, "Password must be at least 6 characters.", http.StatusBadRequest)
		return
	}
	if err := s.store.CreateUser(username, password, email); err != nil {
		http.Error(w, "Registration failed: username or email may already be in use.", http.StatusBadRequest)
		log.Printf("ERROR: failed to create user: %v", err)
		return
	}
	http.Redirect(w, r, "/login.html", http.StatusFound)
}

func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		http.Error(w, "Username and password are required.", http.StatusBadRequest)
		return
	}

	ok, err := s.store.AuthenticateUser(username, password)
	if err != nil || !ok {
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}

	session, err := s.cookieStore.Get(r, "conveyor-session")
	if err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		log.Printf("ERROR: failed to get session: %v", err)
		return
	}
	session.Values["authenticated"] = true
	session.Values["username"] = username
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Failed to save session", http.StatusInternalServerError)
		log.Printf("ERROR: failed to save session: %v", err)
		return
	}

	http.Redirect(w, r, "/repos/index.html", http.StatusFound)
}

func (s *Server) logoutHandler(w http.ResponseWriter, r *http.Request) {
	session, err := s.cookieStore.Get(r, "conveyor-session")
	if err != nil {
		http.Error(w, "Failed to get session", http.StatusInternalServerError)
		log.Printf("ERROR: failed to get session: %v", err)
		return
	}
	session.Values["authenticated"] = false
	session.Options.MaxAge = -1
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Failed to save session", http.StatusInternalServerError)
		log.Printf("ERROR: failed to save session: %v", err)
		return
	}
	http.Redirect(w, r, "/login.html", http.StatusFound)
}

// TODO: fix that all auth things after Vue UI will be done
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := s.cookieStore.Get(r, "conveyor-session")
		if err != nil {
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}
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
	if err := json.NewEncoder(w).Encode(repos); err != nil {
		log.Printf("ERROR: failed to encode repos: %v", err)
	}
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
	if err := json.NewEncoder(w).Encode(job); err != nil {
		log.Printf("ERROR: failed to encode pipeline details: %v", err)
	}
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
	if _, err := w.Write([]byte(logs)); err != nil {
		log.Printf("ERROR: failed to write logs to response: %v", err)
	}
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
	if _, err := w.Write([]byte(svg)); err != nil {
		log.Printf("ERROR: failed to write badge SVG to response: %v", err)
	}
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
	// user.PasswordHash = ""

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(user); err != nil {
		log.Printf("ERROR: failed to encode user profile: %v", err)
	}
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
	if err := json.NewEncoder(w).Encode(orgs); err != nil {
		log.Printf("ERROR: failed to encode user organizations: %v", err)
	}
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
	if err := json.NewEncoder(w).Encode(users); err != nil {
		log.Printf("ERROR: failed to encode user list: %v", err)
	}
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
	if err := json.NewEncoder(w).Encode(jobReq); err != nil {
		log.Printf("ERROR: failed to encode requested job: %v", err)
	}
}

func (s *Server) updateJobHandler(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimPrefix(r.URL.Path, "/api/jobs/update/")
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
	repoFilter := r.URL.Query().Get("repo")

	jobs, err := s.store.ListJobs(repoFilter)
	if err != nil {
		http.Error(w, "Failed to retrieve jobs", http.StatusInternalServerError)
		log.Printf("ERROR: listJobsHandler: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(jobs); err != nil {
		log.Printf("ERROR: failed to encode job list: %v", err)
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
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			log.Printf("ERROR: failed to remove temp directory %s: %v", tempDir, err)
		}
	}()
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

// GET /auth/{forge}/login
func (s *Server) forgeLoginHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 3 {
		http.Error(w, "Invalid auth URL", http.StatusBadRequest)
		return
	}
	forgeType := parts[2]

	oauthCfg, ok := s.oauthConfigs[forgeType]
	if !ok {
		http.Error(w, fmt.Sprintf("Forge '%s' is not configured or enabled on this server.", forgeType), http.StatusNotFound)
		return
	}

	state := uuid.New().String()
	http.SetCookie(w, &http.Cookie{Name: "oauthstate", Value: state, Path: "/", MaxAge: 300, HttpOnly: true})

	url := oauthCfg.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// GET /auth/{forge}/callback
func (s *Server) forgeCallbackHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid callback URL", http.StatusBadRequest)
		return
	}
	forgeType := parts[2]
	oauthCfg, ok := s.oauthConfigs[forgeType]
	if !ok {
		http.Error(w, fmt.Sprintf("Forge '%s' is not configured or enabled on this server.", forgeType), http.StatusNotFound)
		return
	}

	stateCookie, err := r.Cookie("oauthstate")
	if err != nil || r.FormValue("state") != stateCookie.Value {
		http.Error(w, "Invalid state", http.StatusBadRequest)
		return
	}

	code := r.FormValue("code")
	token, err := oauthCfg.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "Failed to exchange OAuth token", http.StatusInternalServerError)
		log.Printf("ERROR: oauth exchange for %s failed: %v", forgeType, err)
		return
	}

	username, err := s.getUsernameFromSession(r)
	if err != nil {
		http.Error(w, "Not authenticated. Please log in before connecting a forge.", http.StatusUnauthorized)
		return
	}
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		http.Error(w, "Authenticated user not found.", http.StatusInternalServerError)
		log.Printf("ERROR: could not find user '%s' from session: %v", username, err)
		return
	}

	if err := s.store.SaveUserConnection(user.ID, forgeType, token.AccessToken, token.RefreshToken, token.Expiry); err != nil {
		http.Error(w, "Failed to save connection", http.StatusInternalServerError)
		log.Printf("ERROR: failed to save connection for user %d: %v", user.ID, err)
		return
	}

	http.Redirect(w, r, "/settings/index.html", http.StatusFound)
}

// GET /api/user/connections
func (s *Server) listUserConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	username, err := s.getUsernameFromSession(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		http.Error(w, "User not found", http.StatusInternalServerError)
		log.Printf("ERROR: could not find user '%s' from session: %v", username, err)
		return
	}
	connections, err := s.store.ListUserConnections(user.ID)
	if err != nil {
		http.Error(w, "Failed to get user connections", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(connections); err != nil {
		log.Printf("ERROR: failed to encode user connections: %v", err)
	}
}

// GET /api/user/source-repos
func (s *Server) listSourceReposHandler(w http.ResponseWriter, r *http.Request) {
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
	connections, err := s.store.ListUserConnections(user.ID)
	if err != nil {
		http.Error(w, "Failed to retrieve user connections", http.StatusInternalServerError)
		log.Printf("ERROR: could not get connections for user %d: %v", user.ID, err)
		return
	}

	allSourceRepos := make([]shared.SourceRepo, 0)
	for _, conn := range connections {
		if conn.ForgeType == "github" {
			// Create an authenticated GitHub client
			ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: conn.AccessToken})
			tc := oauth2.NewClient(r.Context(), ts)
			client := github.NewClient(tc)

			// Call the forge's API to list repositories
			// TODO: List is deprecated, need replacement later
			repos, _, err := client.Repositories.List(r.Context(), "", &github.RepositoryListOptions{
				ListOptions: github.ListOptions{PerPage: 100}, // List up to 100 repos
			})
			if err != nil {
				log.Printf("ERROR: failed to list GitHub repos for user %d: %v", user.ID, err)
				// just skip this forge but no failing
				continue
			}

			for _, repo := range repos {
				if repo.FullName != nil && repo.CloneURL != nil {
					allSourceRepos = append(allSourceRepos, shared.SourceRepo{
						FullName:    *repo.FullName,
						Description: repo.GetDescription(),
						ForgeType:   "github",
						CloneURL:    *repo.CloneURL,
					})
				}
			}
		}
		// TODO: add else blocks for other forges
		// example: `else if conn.ForgeType == "gitlab"`
	}

	// return as JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(allSourceRepos); err != nil {
		log.Printf("ERROR: failed to encode source repos: %v", err)
	}
}

// Activates a single repository, adding it to Conveyor's database.
// POST /api/repos/activate
func (s *Server) activateRepoHandler(w http.ResponseWriter, r *http.Request) {
	username, err := s.getUsernameFromSession(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var reqBody struct {
		RepoFullName string `json:"repo_full_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if reqBody.RepoFullName == "" {
		http.Error(w, "repo_full_name is required", http.StatusBadRequest)
		return
	}

	user, err := s.store.GetUserByUsername(username)
	if err != nil {
		http.Error(w, "User not found", http.StatusInternalServerError)
		return
	}
	connections, err := s.store.ListUserConnections(user.ID)
	if err != nil {
		http.Error(w, "Could not list user connections", http.StatusInternalServerError)
		return
	}

	var githubToken string
	for _, conn := range connections {
		if conn.ForgeType == "github" {
			githubToken = conn.AccessToken
			break
		}
	}
	if githubToken == "" {
		http.Error(w, "GitHub account not connected.", http.StatusBadRequest)
		return
	}

	// Create an authenticated GitHub client
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: githubToken})
	tc := oauth2.NewClient(r.Context(), ts)
	ghClient := github.NewClient(tc)

	// Fetch the specific repository details from GitHub API
	parts := strings.Split(reqBody.RepoFullName, "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid repository name format. Expected 'owner/repo'", http.StatusBadRequest)
		return
	}
	repo, _, err := ghClient.Repositories.Get(r.Context(), parts[0], parts[1])
	if err != nil {
		http.Error(w, "Could not find repository on GitHub or you don't have access.", http.StatusNotFound)
		return
	}

	_, err = s.store.ActivateRepository(*repo.FullName, *repo.CloneURL, *repo.Owner.Login, *repo.Name)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			http.Error(w, "Repository is already activated.", http.StatusConflict)
		} else {
			http.Error(w, "Failed to activate repository in database.", http.StatusInternalServerError)
			log.Printf("ERROR: activateRepoHandler: %v", err)
		}
		return
	}

	// TODO: Create the webhook on the repo using the ghClient

	log.Printf("User '%s' activated repository '%s'", username, reqBody.RepoFullName)
	w.WriteHeader(http.StatusCreated) // 201 Created is the correct status here
}

// POST /api/pipelines/{id}/restart
func (s *Server) restartPipelineHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid URL path", http.StatusBadRequest)
		return
	}
	jobID := parts[3]

	originalJob, err := s.store.GetPipelineByID(jobID)
	if err != nil {
		http.Error(w, "Original job not found", http.StatusNotFound)
		return
	}

	// Create a new job with the same details but a new UUID
	newJobID := uuid.New().String()
	err = s.store.QueueJob(
		newJobID,
		originalJob.Job,
		originalJob.RepoName,
		originalJob.CommitMessage,
		originalJob.CommitSHA,
		originalJob.CommitRef,
		originalJob.CommitAuthor,
	)
	if err != nil {
		http.Error(w, "Failed to queue restarted job", http.StatusInternalServerError)
		log.Printf("ERROR: could not restart job %s: %v", jobID, err)
		return
	}

	log.Printf("User restarted job %s as new job %s", jobID, newJobID)
	w.WriteHeader(http.StatusOK)
}

// GET /api/pipelines/{id}/artifacts
func (s *Server) listArtifactsHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid URL path", http.StatusBadRequest)
		return
	}
	jobID := parts[3]
	artifacts, err := s.store.ListArtifactsForJob(jobID)
	if err != nil {
		http.Error(w, "Failed to list artifacts", http.StatusInternalServerError)
		log.Printf("ERROR: could not list artifacts for job %s: %v", jobID, err)
		return
	}
	if artifacts == nil {
		artifacts = make([]shared.Artifact, 0) // Return empty array instead of null
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(artifacts); err != nil {
		log.Printf("ERROR: failed to encode artifacts list: %v", err)
	}
}

// GET /api/artifacts/download/{id}
func (s *Server) downloadArtifactHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.Error(w, "Invalid artifact ID in URL", http.StatusBadRequest)
		return
	}
	artifactIDStr := parts[4]
	artifactID, err := strconv.Atoi(artifactIDStr)
	if err != nil {
		http.Error(w, "Invalid artifact ID", http.StatusBadRequest)
		return
	}

	artifact, err := s.store.GetArtifactByID(artifactID)
	if err != nil {
		http.Error(w, "Artifact not found", http.StatusNotFound)
		return
	}

	// Securely serve the file from disk
	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(artifact.Filename))
	http.ServeFile(w, r, artifact.StoragePath)
}

// POST /api/jobs/{id}/artifacts
func (s *Server) uploadArtifactsHandler(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimPrefix(r.URL.Path, "/api/artifacts/upload/")

	// 32 << 20 specifies a max upload size of 32 MB
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Could not parse multipart form: file may be too large", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("artifacts")
	if err != nil {
		http.Error(w, "Invalid file key 'artifacts' in form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	storageDir := filepath.Join("data", "artifacts", jobID)
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		http.Error(w, "Could not create storage directory", http.StatusInternalServerError)
		return
	}
	storagePath := filepath.Join(storageDir, handler.Filename)

	dst, err := os.Create(storagePath)
	if err != nil {
		http.Error(w, "Could not create destination file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "Could not save file", http.StatusInternalServerError)
		return
	}

	// Record the artifact in the database
	err = s.store.CreateArtifact(jobID, handler.Filename, handler.Size, storagePath)
	if err != nil {
		http.Error(w, "Could not record artifact in database", http.StatusInternalServerError)
		log.Printf("ERROR: could not create artifact record for job %s: %v", jobID, err)
		return
	}

	log.Printf("Successfully stored artifacts for job %s at %s", jobID, storagePath)
	w.WriteHeader(http.StatusOK)
}

func main() {
	configPath := flag.String("config", "", "Path to the configuration file (optional)")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("FATAL: Failed to load configuration: %v", err)
	}
	log.Printf("Configuration loaded from %s", cfg.ResolvedPath)

	db, err := store.NewStore(cfg.Server.Database)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize database: %v", err)
	}

	server := NewServer(db, cfg)
	mux := http.NewServeMux()
	uiHandler := serverui.New()

	mux.Handle("/api/register", server.rateLimitMiddleware(http.HandlerFunc(server.registrationHandler)))
	mux.Handle("/api/login", server.rateLimitMiddleware(http.HandlerFunc(server.loginHandler)))
	mux.HandleFunc("/api/badges/", server.badgeHandler)
	mux.Handle("/login.html", uiHandler)
	mux.Handle("/register.html", uiHandler)
	mux.Handle("/404.html", uiHandler)
	mux.HandleFunc("/auth/github/login", server.forgeLoginHandler)
	mux.HandleFunc("/auth/github/callback", server.forgeCallbackHandler)

	mux.HandleFunc("/api/jobs/request", server.requestJobHandler)
	mux.HandleFunc("/api/jobs/update/", server.updateJobHandler)
	mux.HandleFunc("/api/artifacts/upload/", server.uploadArtifactsHandler)
	mux.HandleFunc("/webhooks/github", server.githubWebhookHandler)

	privateMux := http.NewServeMux()

	privateMux.HandleFunc("/api/logout", server.logoutHandler)
	privateMux.HandleFunc("/api/jobs", server.listJobsHandler)
	privateMux.HandleFunc("/api/repositories", server.listReposHandler)
	privateMux.HandleFunc("/api/logs/", server.logsHandler)
	privateMux.HandleFunc("/api/user/profile", server.userProfileHandler)
	privateMux.HandleFunc("/api/user/orgs", server.listUserOrgsHandler)
	privateMux.HandleFunc("/api/orgs", server.createOrgHandler)
	privateMux.HandleFunc("/api/user/connections", server.listUserConnectionsHandler)
	privateMux.HandleFunc("/api/user/source-repos", server.listSourceReposHandler)
	privateMux.HandleFunc("/api/repos/activate", server.activateRepoHandler)
	privateMux.HandleFunc("/api/artifacts/download/", server.downloadArtifactHandler)
	privateMux.Handle("/api/admin/users", server.adminOnlyMiddleware(http.HandlerFunc(server.adminListUsersHandler)))

	privateMux.HandleFunc("/api/pipelines/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/restart") && r.Method == http.MethodPost {
			server.restartPipelineHandler(w, r)
		} else if strings.HasSuffix(path, "/artifacts") && r.Method == http.MethodGet {
			server.listArtifactsHandler(w, r)
		} else if r.Method == http.MethodGet {
			// Assume any other GET is for the pipeline detail, since idk
			server.pipelineDetailHandler(w, r)
		} else {
			// Handle any other methods with a 404
			http.NotFound(w, r)
		}
	})

	privateMux.Handle("/", uiHandler)
	mux.Handle("/", server.authMiddleware(privateMux))

	port := cfg.Server.Port
	log.Printf("Starting Conveyor Server on port %s", port)
	log.Printf("Access the UI at http://localhost:%s", port)

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

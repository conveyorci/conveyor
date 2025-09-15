package pipeline

// Pipeline is the top-level structure, now containing a map of jobs.
type Pipeline struct {
	Name string         `yaml:"name"`
	Jobs map[string]Job `yaml:"jobs"`
}

// Job represents a single, isolated execution environment (a single container).
type Job struct {
	Executor string   `yaml:"executor,omitempty"`
	Image    string   `yaml:"image"`
	Commands []string `yaml:"commands"`
}

package runner

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/conveyorci/conveyor/internal/pipeline"
	"gopkg.in/yaml.v3"
)

// Run reads and executes the pipeline defined in the given YAML file.
func Run(configFile string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("could not read config file '%s': %w", configFile, err)
	}

	var p pipeline.Pipeline
	if err := yaml.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("could not parse yaml in '%s': %w", configFile, err)
	}

	log.Printf("Starting pipeline: '%s'", p.Name)

	dockerExec, err := NewDockerExecutor()
	if err != nil {
		return fmt.Errorf("could not create docker executor: %w", err)
	}
	// TODO: implement Firecracker MicroVM support
	// firecrackerExec, _ := NewFirecrackerExecutor()

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not get working directory: %w", err)
	}

	for jobName, job := range p.Jobs {
		var executor Executor
		var executorName string

		switch job.Executor {
		case "docker", "": // Default to Docker if executor is not specified
			executor = dockerExec
			executorName = "docker"
		default:
			return fmt.Errorf("unknown executor type '%s' for job '%s'", job.Executor, jobName)
		}

		log.Printf("--- Starting Job: '%s' (Executor: %s) ---", jobName, executorName)
		if err := executor.Execute(context.Background(), job, workspace); err != nil {
			log.Printf("!!! Job Failed: '%s' !!!", jobName)
			return fmt.Errorf("job '%s' failed: %w", jobName, err)
		}
		log.Printf("--- Job Succeeded: '%s' ---", jobName)
	}

	log.Println("Pipeline completed successfully!")
	return nil
}

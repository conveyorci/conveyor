package runner

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/conveyorci/conveyor/internal/pipeline"
	_ "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// GvisorRuntimeName is the name of the runtime configured in /etc/docker/daemon.json
const GvisorRuntimeName = "runsc"

// DockerExecutor implements the Executor interface for Docker.
type DockerExecutor struct {
	client *client.Client
}

// NewDockerExecutor creates a new DockerExecutor and initializes the Docker client.
func NewDockerExecutor() (*DockerExecutor, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("could not create docker client: %w", err)
	}
	return &DockerExecutor{client: cli}, nil
}

// Execute runs a full job, with multiple commands, inside a single gVisor-sandboxed container.
func (e *DockerExecutor) Execute(ctx context.Context, job pipeline.Job, workspace string) error {
	// 1. Pull the image
	log.Printf("Pulling image: %s", job.Image)
	pullReader, err := e.client.ImagePull(ctx, job.Image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("could not pull image '%s': %w", job.Image, err)
	}
	defer func(pullReader io.ReadCloser) {
		err := pullReader.Close()
		if err != nil {
			log.Printf("could not close pull reader: %v", err)
		}
	}(pullReader)
	if _, err := io.Copy(io.Discard, pullReader); err != nil {
		log.Printf("WARN: could not fully read image pull progress: %v", err)
	}

	// 2. Create the container
	log.Printf("Creating container with mandatory '%s' runtime", GvisorRuntimeName)
	resp, err := e.client.ContainerCreate(ctx, &container.Config{
		Image:      job.Image,
		Cmd:        []string{"sleep", "infinity"},
		WorkingDir: "/app",
	}, &container.HostConfig{
		Binds:   []string{fmt.Sprintf("%s:/app", workspace)},
		Runtime: GvisorRuntimeName,
	}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("could not create container: %w", err)
	}

	// Ensure cleanup
	defer func() {
		log.Printf("Stopping and removing container %s", resp.ID[:12])
		if err := e.client.ContainerStop(ctx, resp.ID, container.StopOptions{}); err != nil {
			log.Printf("WARN: could not stop container %s: %v", resp.ID, err)
		}
		if err := e.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{}); err != nil {
			log.Printf("WARN: could not remove container %s: %v", resp.ID, err)
		}
	}()

	// 3. Start the container
	if err := e.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("could not start container %s: %w", resp.ID, err)
	}

	// 4. Execute each command
	for _, cmdStr := range job.Commands {
		log.Printf("==> Executing: %s", cmdStr)

		// Exec create
		execIDResp, err := e.client.ContainerExecCreate(ctx, resp.ID, container.ExecOptions{
			Cmd:          strslice.StrSlice{"sh", "-c", cmdStr},
			AttachStdout: true,
			AttachStderr: true,
			WorkingDir:   "/app",
		})
		if err != nil {
			return fmt.Errorf("failed to create exec for command '%s': %w", cmdStr, err)
		}

		// Exec attach
		hijackedResp, err := e.client.ContainerExecAttach(ctx, execIDResp.ID, container.ExecStartOptions{})
		if err != nil {
			return fmt.Errorf("failed to attach to exec for command '%s': %w", cmdStr, err)
		}

		func() {
			defer hijackedResp.Close()
			if _, err := stdcopy.StdCopy(os.Stdout, os.Stderr, hijackedResp.Reader); err != nil {
				log.Printf("failed to read exec output for command '%s': %v", cmdStr, err)
			}
		}()

		// Inspect exit code
		inspectResp, err := e.client.ContainerExecInspect(ctx, execIDResp.ID)
		if err != nil {
			return fmt.Errorf("failed to inspect exec for command '%s': %w", cmdStr, err)
		}

		if inspectResp.ExitCode != 0 {
			return fmt.Errorf("command '%s' failed with exit code %d", cmdStr, inspectResp.ExitCode)
		}
	}

	return nil
}

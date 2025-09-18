package runner

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/conveyorci/conveyor/internal/pipeline"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

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

// Execute runs a full job, with multiple commands, inside a single container.
func (e *DockerExecutor) Execute(ctx context.Context, job pipeline.Job, workspace string, logWriter io.Writer) error {
	_, err := fmt.Fprintf(logWriter, "--- Pulling image: %s ---\n", job.Image)
	if err != nil {
		return err
	}
	pullReader, err := e.client.ImagePull(ctx, job.Image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("could not pull image '%s': %w", job.Image, err)
	}
	defer pullReader.Close()
	io.Copy(io.Discard, pullReader)

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

	defer func() {
		log.Printf("Stopping and removing container %s", resp.ID[:12])
		if err := e.client.ContainerStop(ctx, resp.ID, container.StopOptions{}); err != nil {
			log.Printf("WARN: could not stop container %s: %v", resp.ID, err)
		}
		if err := e.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{}); err != nil {
			log.Printf("WARN: could not remove container %s: %v", resp.ID, err)
		}
	}()

	if err := e.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("could not start container %s: %w", resp.ID, err)
	}

	for _, cmdStr := range job.Commands {
		fmt.Fprintf(logWriter, "\n\n$ %s\n", cmdStr)

		execIDResp, err := e.client.ContainerExecCreate(ctx, resp.ID, container.ExecOptions{
			Cmd:          strslice.StrSlice{"sh", "-c", cmdStr},
			AttachStdout: true,
			AttachStderr: true,
			WorkingDir:   "/app",
		})
		if err != nil {
			return fmt.Errorf("failed to create exec for command '%s': %w", cmdStr, err)
		}

		hijackedResp, err := e.client.ContainerExecAttach(ctx, execIDResp.ID, container.ExecStartOptions{})
		if err != nil {
			return fmt.Errorf("failed to attach to exec for command '%s': %w", cmdStr, err)
		}

		err = func() error {
			defer hijackedResp.Close()
			if _, err := stdcopy.StdCopy(logWriter, logWriter, hijackedResp.Reader); err != nil {
				return fmt.Errorf("failed to read exec output for command '%s': %w", cmdStr, err)
			}
			return nil
		}()
		if err != nil {
			return err
		}

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

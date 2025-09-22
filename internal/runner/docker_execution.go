package runner

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"archive/zip"
	"bytes"
	"io/fs"
	"path/filepath"

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

// CreateArtifactsArchive finds files/dirs specified in the artifact paths,
// zips them up, and returns the zip content as a byte buffer.
func CreateArtifactsArchive(workspace string, paths []string) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)
	defer zipWriter.Close()

	for _, path := range paths {
		fullPath := filepath.Join(workspace, path)

		err := filepath.Walk(fullPath, func(filePath string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}

			// path for the file inside the zip
			relPath, err := filepath.Rel(workspace, filePath)
			if err != nil {
				return err
			}

			// zipppy zip
			zipFile, err := zipWriter.Create(relPath)
			if err != nil {
				return err
			}

			fsFile, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer fsFile.Close()

			// copy
			_, err = io.Copy(zipFile, fsFile)
			return err
		})

		if err != nil {
			log.Printf("WARN: Could not process artifact path '%s': %v", path, err)
			// don't fail the whole job
		}
	}

	return buf, nil
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

	if len(job.Artifacts.Paths) > 0 {
		fmt.Fprintln(logWriter, "\n--- Archiving artifacts ---")

		archive, err := CreateArtifactsArchive(workspace, job.Artifacts.Paths)
		if err != nil {
			return fmt.Errorf("failed to create artifacts archive: %w", err)
		}

		if archive.Len() > 0 {
			// TODO: implement agent's upload function
			filename := fmt.Sprintf("artifacts_%s.zip", time.Now().Unix())
			err := os.WriteFile(filename, archive.Bytes(), 0644)
			if err != nil {
				return fmt.Errorf("failed to save artifact archive: %w", err)
			}
			fmt.Fprintf(logWriter, "Successfully created artifact archive: %s\n", filename)
		} else {
			fmt.Fprintln(logWriter, "No artifact files found matching the specified paths.")
		}
	}

	return nil
}

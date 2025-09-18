package runner

import (
	"context"
	"io"

	"github.com/conveyorci/conveyor/internal/pipeline"
)

// Executor defines the contract for running a pipeline step.
// Any execution engine (Docker, Firecracker, etc.) must satisfy this interface.
type Executor interface {
	Execute(ctx context.Context, job pipeline.Job, workspace string, logWriter io.Writer) error
}

package runner

import (
	"context"

	"github.com/conveyorci/conveyor/internal/pipeline"
)

// Executor defines the contract for running a pipeline step.
// Any execution engine (Docker, Firecracker, etc.) must satisfy this interface.
type Executor interface {
	// Execute now takes a Job, which contains multiple commands.
	Execute(ctx context.Context, job pipeline.Job, workspace string) error
}

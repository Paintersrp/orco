package docker

import (
	"context"
	"fmt"

	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
)

type runtimeImpl struct{}

// New returns a runtime adapter placeholder for Docker based workloads. The
// implementation currently surfaces an informative error because integrating
// with a container runtime is outside the scope of these changes.
func New() runtime.Runtime {
	return &runtimeImpl{}
}

func (r *runtimeImpl) Start(ctx context.Context, name string, svc *stack.Service) (runtime.Instance, error) {
	return nil, fmt.Errorf("docker runtime for service %s is not implemented", name)
}

package cli

import (
	stdcontext "context"
	"errors"
	"testing"

	"github.com/Paintersrp/orco/internal/api"
)

func TestControlAPI_NilGuards(t *testing.T) {
	var ctrl *ControlAPI

	if _, err := ctrl.RestartService(stdcontext.Background(), "example"); !errors.Is(err, api.ErrNoActiveDeployment) {
		t.Fatalf("expected ErrNoActiveDeployment for RestartService, got %v", err)
	}

	if _, err := ctrl.Apply(stdcontext.Background()); !errors.Is(err, api.ErrNoActiveDeployment) {
		t.Fatalf("expected ErrNoActiveDeployment for Apply, got %v", err)
	}
}

package httpapi

import (
	stdcontext "context"
	"testing"

	"github.com/Paintersrp/orco/internal/api"
)

type testController struct{}

func (t *testController) Status(stdcontext.Context) (*api.StatusReport, error) {
	return nil, nil
}

func (t *testController) RestartService(stdcontext.Context, string) (*api.RestartResult, error) {
	return nil, nil
}

func (t *testController) Apply(stdcontext.Context) (*api.ApplyResult, error) {
	return nil, nil
}

func TestNewServerRejectsTypedNilController(t *testing.T) {
	var ctrl api.Controller = (*testController)(nil)
	_, err := NewServer(Config{Controller: ctrl})
	if err == nil {
		t.Fatalf("expected error when controller is typed nil")
	}
}

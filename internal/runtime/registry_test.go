package runtime_test

import (
	"testing"

	"github.com/Paintersrp/orco/internal/runtime"
	_ "github.com/Paintersrp/orco/internal/runtime/docker"
	_ "github.com/Paintersrp/orco/internal/runtime/process"
	_ "github.com/Paintersrp/orco/internal/runtime/proxy"
)

func TestNewRegistryContainsBuiltInRuntimes(t *testing.T) {
	reg := runtime.NewRegistry()

	for _, key := range []string{"docker", "podman", "process", "proxy"} {
		if _, ok := reg[key]; !ok {
			t.Fatalf("expected registry to contain %q runtime", key)
		}
	}
}

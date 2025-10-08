package docker

import (
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/container"

	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/runtime/containerutil"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestBuildConfigsVolumes(t *testing.T) {
	spec := runtime.StartSpec{
		Image: "example",
		Volumes: []runtime.Volume{
			{Source: "/host/data", Target: "/var/lib/data"},
			{Source: "/host/cache", Target: "/cache", Mode: "ro"},
		},
	}
	common, err := containerutil.PrepareCommonSpec(spec)
	if err != nil {
		t.Fatalf("PrepareCommonSpec returned error: %v", err)
	}
	_, hostCfg, err := buildConfigs(spec, common)
	if err != nil {
		t.Fatalf("buildConfigs returned error: %v", err)
	}

	want := []string{"/host/data:/var/lib/data", "/host/cache:/cache:ro"}
	if got := hostCfg.Binds; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected binds: got %v want %v", got, want)
	}
}

func TestBuildConfigsVolumeParseError(t *testing.T) {
	spec := runtime.StartSpec{
		Image: "example",
		Volumes: []runtime.Volume{
			{Source: "/host/data", Target: "var/data"},
		},
	}
	if _, err := containerutil.PrepareCommonSpec(spec); err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestBuildConfigsResources(t *testing.T) {
	spec := runtime.StartSpec{
		Image: "example",
		Resources: &stack.Resources{
			CPU:               "500m",
			Memory:            "256Mi",
			MemoryReservation: "128Mi",
		},
	}
	common, err := containerutil.PrepareCommonSpec(spec)
	if err != nil {
		t.Fatalf("PrepareCommonSpec returned error: %v", err)
	}
	_, hostCfg, err := buildConfigs(spec, common)
	if err != nil {
		t.Fatalf("buildConfigs returned error: %v", err)
	}

	if got, want := hostCfg.Resources.NanoCPUs, int64(500_000_000); got != want {
		t.Fatalf("unexpected NanoCPUs: got %d want %d", got, want)
	}
	if got, want := hostCfg.Resources.CPUPeriod, int64(100000); got != want {
		t.Fatalf("unexpected CPUPeriod: got %d want %d", got, want)
	}
	if got, want := hostCfg.Resources.CPUQuota, int64(50000); got != want {
		t.Fatalf("unexpected CPUQuota: got %d want %d", got, want)
	}
	if got, want := hostCfg.Resources.Memory, int64(268435456); got != want {
		t.Fatalf("unexpected memory limit: got %d want %d", got, want)
	}
	if got, want := hostCfg.Resources.MemorySwap, int64(268435456); got != want {
		t.Fatalf("unexpected memory swap limit: got %d want %d", got, want)
	}
	if got, want := hostCfg.Resources.MemoryReservation, int64(134217728); got != want {
		t.Fatalf("unexpected memory reservation: got %d want %d", got, want)
	}
}

func TestBuildConfigsResourcesInvalid(t *testing.T) {
	spec := runtime.StartSpec{
		Image: "example",
		Resources: &stack.Resources{
			CPU: "bogus",
		},
	}
	if _, err := containerutil.PrepareCommonSpec(spec); err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestWaitOutcomeExitErrorOOM(t *testing.T) {
	outcome := waitOutcome{
		status:      container.WaitResponse{StatusCode: 137},
		oomKilled:   true,
		memoryLimit: "256Mi",
	}
	err := waitOutcomeExitError(outcome)
	if err == nil {
		t.Fatalf("expected error for oom exit")
	}
	want := "container terminated by the kernel OOM killer (memory limit 256Mi): container exited with status 137"
	if got := err.Error(); got != want {
		t.Fatalf("unexpected error message: got %q want %q", got, want)
	}
}

package docker

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Paintersrp/orco/internal/runtime"
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
	_, hostCfg, err := buildConfigs(spec)
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
	_, _, err := buildConfigs(spec)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse volume") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "/host/data:var/data") {
		t.Fatalf("error missing bind spec: %v", err)
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
	_, hostCfg, err := buildConfigs(spec)
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
	_, _, err := buildConfigs(spec)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse cpu") {
		t.Fatalf("unexpected error: %v", err)
	}
}

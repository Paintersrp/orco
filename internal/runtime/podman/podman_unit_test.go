package podman

import (
	"testing"

	"github.com/docker/go-connections/nat"

	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/runtime/containerutil"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestBuildSpecVolumes(t *testing.T) {
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
	gen, err := buildSpec(spec, common)
	if err != nil {
		t.Fatalf("buildSpec returned error: %v", err)
	}
	if len(gen.Mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(gen.Mounts))
	}
	if gen.Mounts[0].Source != "/host/data" || gen.Mounts[0].Destination != "/var/lib/data" {
		t.Fatalf("unexpected first mount: %#v", gen.Mounts[0])
	}
	if gen.Mounts[1].Source != "/host/cache" || gen.Mounts[1].Destination != "/cache" {
		t.Fatalf("unexpected second mount: %#v", gen.Mounts[1])
	}
	if len(gen.Mounts[1].Options) == 0 {
		t.Fatalf("expected options for second mount")
	}
}

func TestBuildSpecVolumeParseError(t *testing.T) {
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

func TestBuildSpecResources(t *testing.T) {
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
	gen, err := buildSpec(spec, common)
	if err != nil {
		t.Fatalf("buildSpec returned error: %v", err)
	}
	if gen.ResourceLimits == nil || gen.ResourceLimits.CPU == nil || gen.ResourceLimits.Memory == nil {
		t.Fatalf("expected resource limits to be populated")
	}
	if gen.CPUQuota == 0 || gen.CPUPeriod == 0 {
		t.Fatalf("expected CPU quota and period to be set")
	}
	mem := gen.ResourceLimits.Memory
	if mem.Limit == nil || *mem.Limit != common.Resources.Memory {
		t.Fatalf("unexpected memory limit: %+v", mem)
	}
	if mem.Reservation == nil || *mem.Reservation != common.Resources.MemoryReservation {
		t.Fatalf("unexpected memory reservation: %+v", mem)
	}
}

func TestConvertPortMappings(t *testing.T) {
	spec := runtime.StartSpec{
		Image: "example",
		Ports: []string{"127.0.0.1:8080:80/tcp"},
	}
	common, err := containerutil.PrepareCommonSpec(spec)
	if err != nil {
		t.Fatalf("PrepareCommonSpec returned error: %v", err)
	}
	ports, err := convertPortMappings(common.Ports)
	if err != nil {
		t.Fatalf("convertPortMappings returned error: %v", err)
	}
	if len(ports) != 1 {
		t.Fatalf("expected 1 port mapping, got %d", len(ports))
	}
	mapping := ports[0]
	if mapping.ContainerPort != 80 {
		t.Fatalf("unexpected container port: %d", mapping.ContainerPort)
	}
	if mapping.HostPort != 8080 {
		t.Fatalf("unexpected host port: %d", mapping.HostPort)
	}
	if mapping.Protocol != "tcp" {
		t.Fatalf("unexpected protocol: %s", mapping.Protocol)
	}
	if mapping.HostIP != "127.0.0.1" {
		t.Fatalf("unexpected host ip: %s", mapping.HostIP)
	}
	if mapping.Range != 1 {
		t.Fatalf("unexpected range: %d", mapping.Range)
	}
}

func TestConvertPortMappingsRangeMismatch(t *testing.T) {
	mappings := []containerutil.PortMapping{
		{
			Port:     nat.Port("80-82/tcp"),
			Bindings: []nat.PortBinding{{HostPort: "8080"}},
		},
	}
	if _, err := convertPortMappings(mappings); err == nil {
		t.Fatalf("expected error for mismatched host range")
	}
}

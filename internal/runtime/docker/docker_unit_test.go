package docker

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Paintersrp/orco/internal/runtime"
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

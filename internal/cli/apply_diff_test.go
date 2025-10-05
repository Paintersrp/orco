package cli

import (
	"strings"
	"testing"

	"github.com/Paintersrp/orco/internal/stack"
)

func TestFormatServiceDiffs_NoChanges(t *testing.T) {
	t.Parallel()

	oldSpec := map[string]*stack.Service{
		"api": {
			Image:   "app:v1",
			Command: []string{"run"},
			Env:     map[string]string{"FOO": "bar"},
		},
	}
	diffs, updates, unsupported := diffStackServices(oldSpec, stack.CloneServiceMap(oldSpec))
	if len(diffs) != 0 {
		t.Fatalf("expected no diffs, got %#v", diffs)
	}
	if len(updates) != 0 {
		t.Fatalf("expected no update targets, got %#v", updates)
	}
	if len(unsupported) != 0 {
		t.Fatalf("expected no unsupported services, got %#v", unsupported)
	}
	if got := formatServiceDiffs(diffs); got != "No changes detected." {
		t.Fatalf("unexpected diff output: %q", got)
	}
}

func TestFormatServiceDiffs_ImageCommandEnvChanges(t *testing.T) {
	t.Parallel()

	oldSpec := map[string]*stack.Service{
		"api": {
			Image:   "example/api:v1",
			Command: []string{"serve", "--port", "80"},
			Env: map[string]string{
				"BAR": "old",
				"FOO": "bar",
			},
		},
	}
	newSpec := map[string]*stack.Service{
		"api": {
			Image:   "example/api:v1.1",
			Command: []string{"serve", "--port", "8080"},
			Env: map[string]string{
				"FOO": "baz",
				"ZED": "new",
			},
		},
	}

	diffs, updates, unsupported := diffStackServices(oldSpec, newSpec)
	if len(unsupported) != 0 {
		t.Fatalf("expected no unsupported services, got %#v", unsupported)
	}
	if len(updates) != 1 || updates[0] != "api" {
		t.Fatalf("expected api to be the sole update target, got %#v", updates)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected a single service diff, got %#v", diffs)
	}

	output := formatServiceDiffs(diffs)
	expectations := []string{
		"Service api:",
		"Image: example/api:v1 -> example/api:v1.1",
		"Command: [\"serve\", \"--port\", \"80\"] -> [\"serve\", \"--port\", \"8080\"]",
		"Env:",
		"- BAR",
		"~ FOO: bar -> baz",
		"+ ZED=new",
	}
	for _, expected := range expectations {
		if !strings.Contains(output, expected) {
			t.Fatalf("diff output missing %q:\n%s", expected, output)
		}
	}
}

func TestDiffStackServicesUnsupported(t *testing.T) {
	t.Parallel()

	oldSpec := map[string]*stack.Service{}
	newSpec := map[string]*stack.Service{
		"api": {Image: "app:v1"},
	}

	diffs, updates, unsupported := diffStackServices(oldSpec, newSpec)
	if len(diffs) != 1 {
		t.Fatalf("expected diff for added service, got %#v", diffs)
	}
	if len(updates) != 0 {
		t.Fatalf("expected no update targets for additions, got %#v", updates)
	}
	if len(unsupported) != 1 || unsupported[0] != "api" {
		t.Fatalf("expected api to be marked unsupported, got %#v", unsupported)
	}

	output := formatServiceDiffs(diffs)
	if !strings.Contains(output, "Service api:") || !strings.Contains(output, "Service added.") {
		t.Fatalf("unexpected diff output for added service:\n%s", output)
	}
}

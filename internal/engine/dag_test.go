package engine

import "testing"

func TestBuildGraphNilStackFile(t *testing.T) {
	t.Parallel()

	_, err := BuildGraph(nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if err != errNilStackFile {
		t.Fatalf("unexpected error: %v", err)
	}
}

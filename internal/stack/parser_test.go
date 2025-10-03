package stack

import "testing"

func TestStackFileValidateAcceptsSupportedRuntimes(t *testing.T) {
	tests := map[string]string{
		"docker":  "docker",
		"process": "process",
	}

	for name, runtime := range tests {
		t.Run(name, func(t *testing.T) {
			sf := StackFile{
				Version: "0.1",
				Stack:   StackMeta{Name: "test"},
				Services: ServiceMap{
					"app": {
						Runtime:  runtime,
						Replicas: 1,
					},
				},
			}

			if err := sf.Validate(); err != nil {
				t.Fatalf("expected runtime %s to be accepted, got error: %v", runtime, err)
			}
		})
	}
}

func TestStackFileValidateRejectsUnsupportedRuntime(t *testing.T) {
	sf := StackFile{
		Version: "0.1",
		Stack:   StackMeta{Name: "test"},
		Services: ServiceMap{
			"app": {
				Runtime:  "unsupported",
				Replicas: 1,
			},
		},
	}

	err := sf.Validate()
	if err == nil {
		t.Fatalf("expected error for unsupported runtime")
	}
}

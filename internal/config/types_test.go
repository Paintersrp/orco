package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidatePortSuccess(t *testing.T) {
	cases := []string{
		"8080:8080",
		"127.0.0.1:9090:8080",
		"[2001:db8::1]:8080:8080",
		"0.0.0.0:8443:443/tcp",
		"8080-8081:8080-8081/udp",
	}
	for _, spec := range cases {
		spec := spec
		t.Run(spec, func(t *testing.T) {
			if err := validatePort(spec); err != nil {
				t.Fatalf("validatePort(%q) returned error: %v", spec, err)
			}
		})
	}
}

func TestValidatePortFailures(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want string
	}{
		{name: "missing host port", spec: "8080", want: "host port must be specified"},
		{name: "missing container port", spec: "8080:", want: "No port specified"},
		{name: "invalid host ip", spec: "localhost:8080:80", want: "Invalid ip address"},
		{name: "invalid proto", spec: "127.0.0.1:8080:80/foo", want: "Invalid proto"},
		{name: "host port zero", spec: "0:8080", want: "host port must be in range"},
		{name: "container port zero", spec: "8080:0", want: "container port must be in range"},
		{name: "host ip without port", spec: "127.0.0.1::8080", want: "host port must be specified"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validatePort(tc.spec)
			if err == nil {
				t.Fatalf("validatePort(%q) returned nil error", tc.spec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error for %q: got %q want substring %q", tc.spec, err, tc.want)
			}
		})
	}
}

func TestValidateVolumeSpecSuccess(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "data") + ":/var/lib/data"
	if err := validateVolumeSpec(spec); err != nil {
		t.Fatalf("validateVolumeSpec returned error: %v", err)
	}

	spec = filepath.Join(dir, "cache") + ":/cache:ro"
	if err := validateVolumeSpec(spec); err != nil {
		t.Fatalf("validateVolumeSpec with mode returned error: %v", err)
	}
}

func TestValidateVolumeSpecFailures(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		spec string
		want string
	}{
		{name: "empty", spec: "", want: "volume specification is empty"},
		{name: "format", spec: "/host", want: "expected format"},
		{name: "host relative", spec: "data:/var/lib/data", want: "host path"},
		{name: "container missing", spec: "/host:", want: "container path is required"},
		{name: "container relative", spec: filepath.Join(dir, "data") + ":data", want: "container path"},
		{name: "mode empty", spec: filepath.Join(dir, "data") + ":/var/data:", want: "mode is empty"},
		{name: "mode extra", spec: filepath.Join(dir, "data") + ":/var/data:ro:rw", want: "unexpected ':'"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validateVolumeSpec(tc.spec)
			if err == nil {
				t.Fatalf("validateVolumeSpec(%q) returned nil error", tc.spec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error for %q: got %q want substring %q", tc.spec, err, tc.want)
			}
		})
	}
}

func TestValidateProbeAllowsHTTPAndLogWithExpression(t *testing.T) {
	probe := &ProbeSpec{
		HTTP:       &HTTPProbeSpec{URL: "http://localhost:8080/health"},
		Log:        &LogProbeSpec{Pattern: "ready"},
		Expression: "http || log",
	}
	if err := validateProbe("api", probe); err != nil {
		t.Fatalf("validateProbe returned error: %v", err)
	}
	if got, want := probe.FailureThreshold, 3; got != want {
		t.Fatalf("failure threshold default mismatch: got %d want %d", got, want)
	}
}

func TestValidateProbeRequiresExpressionForMultipleProbes(t *testing.T) {
	probe := &ProbeSpec{
		HTTP: &HTTPProbeSpec{URL: "http://localhost:8080/health"},
		Log:  &LogProbeSpec{Pattern: "ready"},
	}
	err := validateProbe("api", probe)
	if err == nil || !strings.Contains(err.Error(), "expression") {
		t.Fatalf("expected expression error, got %v", err)
	}
}

func TestValidateProbeLogPatternMustBeValidRegex(t *testing.T) {
	probe := &ProbeSpec{
		Log: &LogProbeSpec{Pattern: "("},
	}
	err := validateProbe("api", probe)
	if err == nil || !strings.Contains(err.Error(), "invalid pattern") {
		t.Fatalf("expected invalid pattern error, got %v", err)
	}
}

func TestValidateProbeExpressionMustReferenceConfiguredProbe(t *testing.T) {
	probe := &ProbeSpec{
		HTTP:       &HTTPProbeSpec{URL: "http://localhost:8080/health"},
		Expression: "http || tcp",
	}
	err := validateProbe("api", probe)
	if err == nil || !strings.Contains(err.Error(), "undefined probe \"tcp\"") {
		t.Fatalf("expected undefined probe error, got %v", err)
	}
}

func TestParseProbeExpression(t *testing.T) {
	refs, err := parseProbeExpression("http or log or cmd")
	if err != nil {
		t.Fatalf("parseProbeExpression returned error: %v", err)
	}
	want := []string{"http", "log", "cmd"}
	if len(refs) != len(want) {
		t.Fatalf("unexpected refs length: got %d want %d", len(refs), len(want))
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("unexpected ref at %d: got %q want %q", i, refs[i], want[i])
		}
	}
}

func TestParseProbeExpressionErrors(t *testing.T) {
	cases := map[string]string{
		"":               "empty",
		"http and log":   "unsupported operator",
		"http ||":        "incomplete",
		"unknown or log": "invalid probe reference",
	}
	for expr, want := range cases {
		_, err := parseProbeExpression(expr)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("parseProbeExpression(%q) error = %v, want substring %q", expr, err, want)
		}
	}
}

func TestStackValidateBlueGreenSwitchDefaultsToPorts(t *testing.T) {
	stack := blueGreenTestStack("", t)
	if err := stack.Validate(); err != nil {
		t.Fatalf("stack.Validate returned error: %v", err)
	}
	svc := stack.Services["api"]
	if svc == nil || svc.Update == nil || svc.Update.BlueGreen == nil {
		t.Fatalf("expected blueGreen configuration to be present")
	}
	if svc.Update.BlueGreen.Switch != BlueGreenSwitchPorts {
		t.Fatalf("unexpected switch value: got %q want %q", svc.Update.BlueGreen.Switch, BlueGreenSwitchPorts)
	}
}

func TestStackValidateBlueGreenSwitchAllowsProxyLabel(t *testing.T) {
	stack := blueGreenTestStack("Proxy-Label", t)
	if err := stack.Validate(); err != nil {
		t.Fatalf("stack.Validate returned error: %v", err)
	}
	svc := stack.Services["api"]
	if svc.Update.BlueGreen.Switch != BlueGreenSwitchProxyLabel {
		t.Fatalf("unexpected switch value: got %q want %q", svc.Update.BlueGreen.Switch, BlueGreenSwitchProxyLabel)
	}
}

func TestStackValidateBlueGreenSwitchRejectsUnknownValue(t *testing.T) {
	stack := blueGreenTestStack("invalid", t)
	err := stack.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported value") {
		t.Fatalf("expected unsupported value error, got %v", err)
	}
}

func TestStackValidateUpdateRejectsNegativeValues(t *testing.T) {
	mkStack := func() *Stack {
		return &Stack{
			Version: "0.1",
			Stack:   StackMeta{Name: "demo"},
			Services: map[string]*ServiceSpec{
				"api": {
					Runtime:  "docker",
					Image:    "ghcr.io/demo/api:latest",
					Replicas: 1,
					Health: &ProbeSpec{
						HTTP: &HTTPProbeSpec{URL: "http://localhost:8080/health"},
					},
				},
			},
		}
	}

	t.Run("abortAfterFailures", func(t *testing.T) {
		stack := mkStack()
		stack.Services["api"].Update = &UpdateStrategy{AbortAfterFailures: -1}
		err := stack.Validate()
		if err == nil || !strings.Contains(err.Error(), "abortAfterFailures") {
			t.Fatalf("expected abortAfterFailures error, got %v", err)
		}
	})

	t.Run("observationWindow", func(t *testing.T) {
		stack := mkStack()
		stack.Services["api"].Update = &UpdateStrategy{
			ObservationWindow: Duration{Duration: -time.Second, explicit: true},
		}
		err := stack.Validate()
		if err == nil || !strings.Contains(err.Error(), "observationWindow") {
			t.Fatalf("expected observationWindow error, got %v", err)
		}
	})
}

func blueGreenTestStack(switchValue string, t *testing.T) *Stack {
	t.Helper()
	return &Stack{
		Version: "0.1",
		Stack:   StackMeta{Name: "demo"},
		Services: map[string]*ServiceSpec{
			"api": {
				Runtime:  "docker",
				Image:    "example/api:latest",
				Replicas: 1,
				Ports:    []string{"8080:8080"},
				Health: &ProbeSpec{
					HTTP: &HTTPProbeSpec{URL: "http://127.0.0.1:8080/health"},
				},
				Update: &UpdateStrategy{
					Strategy: "blueGreen",
					BlueGreen: &BlueGreenStrategy{
						Switch: switchValue,
					},
				},
			},
		},
	}
}

func TestProbeApplyDefaultsCopiesLogAndExpression(t *testing.T) {
	defaults := &ProbeSpec{
		Interval:   Duration{Duration: time.Second},
		Log:        &LogProbeSpec{Pattern: "ready", Sources: []string{"stderr"}},
		Expression: "http || log",
	}
	probe := &ProbeSpec{}
	probe.ApplyDefaults(defaults)
	if probe.Log == nil {
		t.Fatalf("expected log defaults to be applied")
	}
	if probe.Log.Pattern != "ready" {
		t.Fatalf("unexpected pattern: got %q want %q", probe.Log.Pattern, "ready")
	}
	if got, want := probe.Expression, defaults.Expression; got != want {
		t.Fatalf("unexpected expression: got %q want %q", got, want)
	}
	if len(probe.Log.Sources) != 1 || probe.Log.Sources[0] != "stderr" {
		t.Fatalf("unexpected sources: %#v", probe.Log.Sources)
	}
}

func TestStackValidateLoggingConstraints(t *testing.T) {
	mkStack := func() *Stack {
		return &Stack{
			Version: "0.1",
			Stack:   StackMeta{Name: "demo"},
			Services: map[string]*ServiceSpec{
				"api": {
					Runtime:  "process",
					Command:  []string{"/bin/true"},
					Replicas: 1,
					Health: &ProbeSpec{
						HTTP: &HTTPProbeSpec{URL: "http://localhost:8080/healthz", ExpectStatus: []int{200}},
					},
				},
			},
		}
	}

	cases := []struct {
		name    string
		logging *LoggingSpec
		want    string
	}{
		{
			name:    "negative file size",
			logging: &LoggingSpec{MaxFileSize: int64Ptr(-1)},
			want:    "logging.maxFileSize",
		},
		{
			name:    "negative total size",
			logging: &LoggingSpec{MaxTotalSize: int64Ptr(-1)},
			want:    "logging.maxTotalSize",
		},
		{
			name:    "negative file count",
			logging: &LoggingSpec{MaxFileCount: intPtr(-1)},
			want:    "logging.maxFileCount",
		},
		{
			name:    "negative file age",
			logging: &LoggingSpec{MaxFileAge: Duration{Duration: -time.Second, explicit: true}},
			want:    "logging.maxFileAge",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stack := mkStack()
			stack.Logging = tc.logging
			err := stack.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestStackValidateAllowsPodmanRuntime(t *testing.T) {
	stack := &Stack{
		Version: "0.1",
		Stack:   StackMeta{Name: "demo"},
		Services: map[string]*ServiceSpec{
			"api": {
				Runtime:  "podman",
				Image:    "ghcr.io/demo/api:latest",
				Replicas: 1,
				Health: &ProbeSpec{
					TCP: &TCPProbeSpec{Address: "localhost:8080"},
				},
			},
		},
	}

	if err := stack.Validate(); err != nil {
		t.Fatalf("stack.Validate returned error: %v", err)
	}
}

func TestStackValidatePodmanRequiresImage(t *testing.T) {
	stack := &Stack{
		Version: "0.1",
		Stack:   StackMeta{Name: "demo"},
		Services: map[string]*ServiceSpec{
			"api": {
				Runtime:  "podman",
				Replicas: 1,
				Health: &ProbeSpec{
					TCP: &TCPProbeSpec{Address: "localhost:8080"},
				},
			},
		},
	}

	err := stack.Validate()
	if err == nil || !strings.Contains(err.Error(), "services.api.image") {
		t.Fatalf("expected image error, got %v", err)
	}
}

func TestStackValidateServiceHooks(t *testing.T) {
	mkStack := func(hooks *ServiceHooks) *Stack {
		return &Stack{
			Version: "0.1",
			Stack:   StackMeta{Name: "demo"},
			Services: map[string]*ServiceSpec{
				"api": {
					Runtime:  "docker",
					Image:    "ghcr.io/demo/api:latest",
					Replicas: 1,
					Health: &ProbeSpec{
						HTTP: &HTTPProbeSpec{URL: "http://localhost:8080/health"},
					},
					Hooks: hooks,
				},
			},
		}
	}

	t.Run("valid", func(t *testing.T) {
		hooks := &ServiceHooks{
			PreStart: &LifecycleHookSpec{
				Command: []string{"/bin/true"},
				Timeout: Duration{Duration: 2 * time.Second, explicit: true},
				Options: map[string]string{"retries": "3"},
			},
		}
		if err := mkStack(hooks).Validate(); err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})

	t.Run("missing command", func(t *testing.T) {
		hooks := &ServiceHooks{PreStart: &LifecycleHookSpec{}}
		err := mkStack(hooks).Validate()
		if err == nil || !strings.Contains(err.Error(), "services.api.hooks.preStart.command") {
			t.Fatalf("expected command error, got %v", err)
		}
	})

	t.Run("empty command argument", func(t *testing.T) {
		hooks := &ServiceHooks{PreStart: &LifecycleHookSpec{Command: []string{""}}}
		err := mkStack(hooks).Validate()
		if err == nil || !strings.Contains(err.Error(), "services.api.hooks.preStart.command[0]") {
			t.Fatalf("expected command argument error, got %v", err)
		}
	})

	t.Run("negative timeout", func(t *testing.T) {
		hooks := &ServiceHooks{PreStart: &LifecycleHookSpec{Command: []string{"/bin/true"}, Timeout: Duration{Duration: -time.Second, explicit: true}}}
		err := mkStack(hooks).Validate()
		if err == nil || !strings.Contains(err.Error(), "services.api.hooks.preStart.timeout") {
			t.Fatalf("expected timeout error, got %v", err)
		}
	})

	t.Run("empty option key", func(t *testing.T) {
		hooks := &ServiceHooks{PreStart: &LifecycleHookSpec{Command: []string{"/bin/true"}, Options: map[string]string{"": "value"}}}
		err := mkStack(hooks).Validate()
		if err == nil || !strings.Contains(err.Error(), "services.api.hooks.preStart.options") {
			t.Fatalf("expected options error, got %v", err)
		}
	})
}

func TestServiceHooksClone(t *testing.T) {
	hooks := &ServiceHooks{
		PreStart: &LifecycleHookSpec{
			Command: []string{"/bin/true", "--flag"},
			Timeout: Duration{Duration: time.Second, explicit: true},
			Options: map[string]string{"foo": "bar"},
		},
		PostStop: &LifecycleHookSpec{Command: []string{"cleanup"}},
	}

	svc := &ServiceSpec{Hooks: hooks}
	clone := svc.Clone()
	if clone.Hooks == nil || clone.Hooks.PreStart == nil {
		t.Fatalf("expected hooks to be cloned")
	}

	clone.Hooks.PreStart.Command[0] = "modified"
	clone.Hooks.PreStart.Options["foo"] = "baz"

	if svc.Hooks.PreStart.Command[0] != "/bin/true" {
		t.Fatalf("unexpected mutation of original command: %q", svc.Hooks.PreStart.Command[0])
	}
	if svc.Hooks.PreStart.Options["foo"] != "bar" {
		t.Fatalf("unexpected mutation of original options: %q", svc.Hooks.PreStart.Options["foo"])
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func intPtr(v int) *int {
	return &v
}

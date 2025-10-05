package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/docker/go-connections/nat"
)

// Duration wraps time.Duration for YAML unmarshalling.
type Duration struct {
	time.Duration
	explicit bool
}

// UnmarshalText parses a textual duration, accepting empty strings.
func (d *Duration) UnmarshalText(text []byte) error {
	d.explicit = true
	if len(text) == 0 {
		d.Duration = 0
		return nil
	}
	dur, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(text), err)
	}
	d.Duration = dur
	return nil
}

// MarshalText renders the duration using time.Duration formatting.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

// IsSet reports whether the duration was explicitly provided or non-zero.
func (d Duration) IsSet() bool {
	return d.explicit || d.Duration != 0
}

// Stack mirrors the stack.yaml document structure.
type Stack struct {
	Version  string                  `yaml:"version"`
	Stack    StackMeta               `yaml:"stack"`
	Defaults Defaults                `yaml:"defaults"`
	Services map[string]*ServiceSpec `yaml:"services"`
}

// StackMeta contains metadata about the stack document.
type StackMeta struct {
	Name    string `yaml:"name"`
	Workdir string `yaml:"workdir"`
}

// Defaults captures default policies applied to services.
type Defaults struct {
	Restart *RestartPolicy `yaml:"restartPolicy"`
	Health  *ProbeSpec     `yaml:"health"`
}

// ServiceSpec describes an individual service in the stack.
type ServiceSpec struct {
	Image           string            `yaml:"image"`
	Runtime         string            `yaml:"runtime"`
	Command         []string          `yaml:"command"`
	Env             map[string]string `yaml:"env"`
	EnvFromFile     string            `yaml:"envFromFile"`
	Ports           []string          `yaml:"ports"`
	Volumes         []string          `yaml:"volumes"`
	DependsOn       []DepEdge         `yaml:"dependsOn"`
	Health          *ProbeSpec        `yaml:"health"`
	Update          *UpdateStrategy   `yaml:"update"`
	Replicas        int               `yaml:"replicas"`
	RestartPolicy   *RestartPolicy    `yaml:"restartPolicy"`
	ResolvedWorkdir string            `yaml:"-"`
}

// DepEdge describes a dependency edge from one service to another.
type DepEdge struct {
	Target  string   `yaml:"target"`
	Require string   `yaml:"require"`
	Timeout Duration `yaml:"timeout"`
}

// ProbeSpec configures readiness probes for a service.
type ProbeSpec struct {
	GracePeriod      Duration       `yaml:"gracePeriod"`
	Interval         Duration       `yaml:"interval"`
	Timeout          Duration       `yaml:"timeout"`
	FailureThreshold int            `yaml:"failureThreshold"`
	SuccessThreshold int            `yaml:"successThreshold"`
	HTTP             *HTTPProbeSpec `yaml:"http"`
	TCP              *TCPProbeSpec  `yaml:"tcp"`
	Command          *CommandProbe  `yaml:"cmd"`
	Log              *LogProbeSpec  `yaml:"log"`
	Expression       string         `yaml:"expression"`
}

// HTTPProbeSpec defines an HTTP probe.
type HTTPProbeSpec struct {
	URL          string `yaml:"url"`
	ExpectStatus []int  `yaml:"expectStatus"`
}

// TCPProbeSpec defines a TCP probe.
type TCPProbeSpec struct {
	Address string `yaml:"address"`
}

// CommandProbe defines a command probe.
type CommandProbe struct {
	Command []string `yaml:"command"`
	Timeout Duration `yaml:"timeout"`
}

// LogProbeSpec defines a log pattern probe.
type LogProbeSpec struct {
	Pattern string   `yaml:"pattern"`
	Sources []string `yaml:"sources"`
	Levels  []string `yaml:"levels"`
}

// UpdateStrategy controls rolling update behaviour.
type UpdateStrategy struct {
	Strategy       string   `yaml:"strategy"`
	MaxUnavailable int      `yaml:"maxUnavailable"`
	MaxSurge       int      `yaml:"maxSurge"`
	PromoteAfter   Duration `yaml:"promoteAfter"`
}

// RestartPolicy defines restart behaviour for a service.
type RestartPolicy struct {
	MaxRetries int          `yaml:"maxRetries"`
	Backoff    *BackoffSpec `yaml:"backoff"`
}

// BackoffSpec describes exponential backoff configuration.
type BackoffSpec struct {
	Min    Duration `yaml:"min"`
	Max    Duration `yaml:"max"`
	Factor float64  `yaml:"factor"`
}

// ApplyDefaults merges defaults onto services.
func (s *Stack) ApplyDefaults() error {
	for name, svc := range s.Services {
		if svc == nil {
			return fmt.Errorf("service %q is null", name)
		}
		if svc.Replicas == 0 {
			svc.Replicas = 1
		}
		if svc.Runtime == "" {
			svc.Runtime = "docker"
		}
		if svc.RestartPolicy == nil && s.Defaults.Restart != nil {
			svc.RestartPolicy = s.Defaults.Restart.Clone()
		}
		if s.Defaults.Health != nil {
			if svc.Health == nil {
				svc.Health = s.Defaults.Health.Clone()
			} else {
				svc.Health.ApplyDefaults(s.Defaults.Health)
			}
		}
	}
	return nil
}

// Validate enforces schema invariants.
func (s *Stack) Validate() error {
	if s.Version == "" {
		return fmt.Errorf("%s: is required", fieldPath("version"))
	}
	if len(s.Services) == 0 {
		return fmt.Errorf("%s: must define at least one service", fieldPath("services"))
	}
	if s.Stack.Name == "" {
		return fmt.Errorf("%s: is required", fieldPath("stack", "name"))
	}
	for name, svc := range s.Services {
		if svc.Runtime == "" {
			return fmt.Errorf("%s: is required", serviceField(name, "runtime"))
		}
		if svc.Runtime != "docker" && svc.Runtime != "process" {
			return fmt.Errorf("%s: unsupported runtime %q (supported values: docker, process)", serviceField(name, "runtime"), svc.Runtime)
		}
		if svc.Runtime == "docker" {
			if strings.TrimSpace(svc.Image) == "" {
				return fmt.Errorf("%s: is required", serviceField(name, "image"))
			}
		}
		if svc.Runtime == "process" {
			if len(svc.Command) == 0 {
				return fmt.Errorf("%s: must contain at least one entry", serviceField(name, "command"))
			}
		}
		if svc.Health == nil {
			return fmt.Errorf("%s: is required", serviceField(name, "health"))
		}
		if err := validateProbe(name, svc.Health); err != nil {
			return err
		}
		if svc.Replicas < 1 {
			return fmt.Errorf("%s: must be at least 1", serviceField(name, "replicas"))
		}
		if svc.RestartPolicy != nil && svc.RestartPolicy.Backoff != nil {
			if svc.RestartPolicy.Backoff.Factor == 0 {
				return fmt.Errorf("%s: must be non-zero", serviceField(name, "restartPolicy", "backoff", "factor"))
			}
		}
		for i, dep := range svc.DependsOn {
			if dep.Target == "" {
				return fmt.Errorf("%s: is required", dependencyField(name, i, "target"))
			}
			switch dep.Require {
			case "", "ready", "started", "exists":
			default:
				return fmt.Errorf("%s: invalid value %q (expected one of: ready, started, exists)", dependencyField(name, i, "require"), dep.Require)
			}
			if _, ok := s.Services[dep.Target]; !ok {
				return fmt.Errorf("%s: references unknown service %q", dependencyField(name, i, "target"), dep.Target)
			}
		}
		for i, port := range svc.Ports {
			if err := validatePort(port); err != nil {
				return fmt.Errorf("%s: %w", serviceField(name, fmt.Sprintf("ports[%d]", i)), err)
			}
		}
		for i, volume := range svc.Volumes {
			if err := validateVolumeSpec(volume); err != nil {
				return fmt.Errorf("%s: %w", serviceField(name, fmt.Sprintf("volumes[%d]", i)), err)
			}
		}
	}
	return nil
}

func validateProbe(name string, p *ProbeSpec) error {
	configured := map[string]bool{}
	probes := 0
	if p.HTTP != nil {
		probes++
		configured["http"] = true
		if p.HTTP.URL == "" {
			return fmt.Errorf("%s: is required", probeField(name, "http", "url"))
		}
	}
	if p.TCP != nil {
		probes++
		configured["tcp"] = true
		if p.TCP.Address == "" {
			return fmt.Errorf("%s: is required", probeField(name, "tcp", "address"))
		}
	}
	if p.Command != nil {
		probes++
		configured["cmd"] = true
		if len(p.Command.Command) == 0 {
			return fmt.Errorf("%s: must contain at least one entry", probeField(name, "cmd", "command"))
		}
	}
	if p.Log != nil {
		probes++
		configured["log"] = true
		if strings.TrimSpace(p.Log.Pattern) == "" {
			return fmt.Errorf("%s: is required", probeField(name, "log", "pattern"))
		}
		if _, err := regexp.Compile(p.Log.Pattern); err != nil {
			return fmt.Errorf("%s: invalid pattern %q: %w", probeField(name, "log", "pattern"), p.Log.Pattern, err)
		}
	}
	if probes == 0 {
		return fmt.Errorf("%s: probe configuration is required", probeField(name))
	}
	if probes > 1 && strings.TrimSpace(p.Expression) == "" {
		return fmt.Errorf("%s: is required when multiple probes are configured", probeField(name, "expression"))
	}
	if strings.TrimSpace(p.Expression) != "" {
		refs, err := parseProbeExpression(p.Expression)
		if err != nil {
			return fmt.Errorf("%s: %w", probeField(name, "expression"), err)
		}
		for _, ref := range refs {
			if !configured[ref] {
				return fmt.Errorf("%s: references undefined probe %q", probeField(name, "expression"), ref)
			}
		}
	}
	if p.FailureThreshold == 0 {
		p.FailureThreshold = 3
	}
	if p.Interval.Duration == 0 {
		p.Interval.Duration = 2 * time.Second
	}
	if p.Timeout.Duration == 0 {
		p.Timeout.Duration = time.Second
	}
	if p.SuccessThreshold == 0 {
		p.SuccessThreshold = 1
	}
	if p.Command != nil && p.Command.Timeout.Duration == 0 {
		p.Command.Timeout = p.Timeout
	}
	return nil
}

func validatePort(spec string) error {
	mappings, err := nat.ParsePortSpec(spec)
	if err != nil {
		return fmt.Errorf("invalid port mapping %q: %w", spec, err)
	}
	if len(mappings) == 0 {
		return fmt.Errorf("invalid port mapping %q: no port definitions found", spec)
	}
	for _, mapping := range mappings {
		hostPort := strings.TrimSpace(mapping.Binding.HostPort)
		if hostPort == "" {
			return fmt.Errorf("invalid port mapping %q: host port must be specified", spec)
		}
		hostStart, hostEnd, err := nat.ParsePortRange(hostPort)
		if err != nil {
			return fmt.Errorf("invalid port mapping %q: invalid host port %q", spec, hostPort)
		}
		if hostStart == 0 || hostEnd == 0 {
			return fmt.Errorf("invalid port mapping %q: host port must be in range 1-65535", spec)
		}
		containerStart, containerEnd, err := mapping.Port.Range()
		if err != nil {
			return fmt.Errorf("invalid port mapping %q: %w", spec, err)
		}
		if containerStart == 0 || containerEnd == 0 {
			return fmt.Errorf("invalid port mapping %q: container port must be in range 1-65535", spec)
		}
	}
	return nil
}

// ApplyDefaults merges values from the provided defaults onto the receiver.
func (p *ProbeSpec) ApplyDefaults(defaults *ProbeSpec) {
	if defaults == nil {
		return
	}
	hasType := p.HTTP != nil || p.TCP != nil || p.Command != nil || p.Log != nil
	if !hasType {
		if p.HTTP == nil && defaults.HTTP != nil {
			p.HTTP = &HTTPProbeSpec{
				URL:          defaults.HTTP.URL,
				ExpectStatus: append([]int(nil), defaults.HTTP.ExpectStatus...),
			}
		}
		if p.TCP == nil && defaults.TCP != nil {
			p.TCP = &TCPProbeSpec{Address: defaults.TCP.Address}
		}
		if p.Command == nil && defaults.Command != nil {
			p.Command = &CommandProbe{
				Command: append([]string(nil), defaults.Command.Command...),
				Timeout: defaults.Command.Timeout,
			}
		}
		if p.Log == nil && defaults.Log != nil {
			p.Log = &LogProbeSpec{
				Pattern: defaults.Log.Pattern,
				Sources: append([]string(nil), defaults.Log.Sources...),
				Levels:  append([]string(nil), defaults.Log.Levels...),
			}
		}
	}
	if !p.GracePeriod.IsSet() {
		p.GracePeriod = defaults.GracePeriod
	}
	if p.Interval.Duration == 0 {
		p.Interval = defaults.Interval
	}
	if p.Timeout.Duration == 0 {
		p.Timeout = defaults.Timeout
	}
	if p.FailureThreshold == 0 {
		p.FailureThreshold = defaults.FailureThreshold
	}
	if p.SuccessThreshold == 0 {
		p.SuccessThreshold = defaults.SuccessThreshold
	}
	if p.Command != nil && defaults.Command != nil {
		if p.Command.Timeout.Duration == 0 {
			p.Command.Timeout = defaults.Command.Timeout
		}
	}
	if strings.TrimSpace(p.Expression) == "" {
		p.Expression = defaults.Expression
	}
}

// Clone creates a deep copy of the service.
func (s *ServiceSpec) Clone() *ServiceSpec {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Env != nil {
		cp.Env = make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			cp.Env[k] = v
		}
	}
	if s.Command != nil {
		cp.Command = append([]string(nil), s.Command...)
	}
	if s.Ports != nil {
		cp.Ports = append([]string(nil), s.Ports...)
	}
	if s.Volumes != nil {
		cp.Volumes = append([]string(nil), s.Volumes...)
	}
	if s.DependsOn != nil {
		cp.DependsOn = append([]DepEdge(nil), s.DependsOn...)
	}
	if s.Health != nil {
		cp.Health = s.Health.Clone()
	}
	if s.Update != nil {
		cp.Update = &UpdateStrategy{
			Strategy:       s.Update.Strategy,
			MaxUnavailable: s.Update.MaxUnavailable,
			MaxSurge:       s.Update.MaxSurge,
			PromoteAfter:   s.Update.PromoteAfter,
		}
	}
	if s.RestartPolicy != nil {
		cp.RestartPolicy = s.RestartPolicy.Clone()
	}
	return &cp
}

// Clone creates a deep copy of the probe configuration.
func (p *ProbeSpec) Clone() *ProbeSpec {
	if p == nil {
		return nil
	}
	cp := *p
	if p.HTTP != nil {
		cp.HTTP = &HTTPProbeSpec{
			URL:          p.HTTP.URL,
			ExpectStatus: append([]int(nil), p.HTTP.ExpectStatus...),
		}
	}
	if p.TCP != nil {
		cp.TCP = &TCPProbeSpec{Address: p.TCP.Address}
	}
	if p.Command != nil {
		cp.Command = &CommandProbe{
			Command: append([]string(nil), p.Command.Command...),
			Timeout: p.Command.Timeout,
		}
	}
	if p.Log != nil {
		cp.Log = &LogProbeSpec{
			Pattern: p.Log.Pattern,
			Sources: append([]string(nil), p.Log.Sources...),
			Levels:  append([]string(nil), p.Log.Levels...),
		}
	}
	return &cp
}

func parseProbeExpression(expr string) ([]string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, fmt.Errorf("expression is empty")
	}
	tokens := strings.Fields(trimmed)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("expression is empty")
	}
	expectProbe := true
	refs := make([]string, 0, (len(tokens)+1)/2)
	for _, token := range tokens {
		lower := strings.ToLower(token)
		if expectProbe {
			switch lower {
			case "http", "tcp", "cmd", "log":
				refs = append(refs, lower)
				expectProbe = false
			default:
				return nil, fmt.Errorf("invalid probe reference %q", token)
			}
			continue
		}
		if lower != "or" && token != "||" {
			return nil, fmt.Errorf("unsupported operator %q", token)
		}
		expectProbe = true
	}
	if expectProbe {
		return nil, fmt.Errorf("expression is incomplete")
	}
	return refs, nil
}

// Clone creates a deep copy of the restart policy.
func (r *RestartPolicy) Clone() *RestartPolicy {
	if r == nil {
		return nil
	}
	cp := *r
	if r.Backoff != nil {
		cp.Backoff = &BackoffSpec{
			Min:    r.Backoff.Min,
			Max:    r.Backoff.Max,
			Factor: r.Backoff.Factor,
		}
	}
	return &cp
}

func fieldPath(parts ...string) string {
	return strings.Join(parts, ".")
}

func serviceField(service string, parts ...string) string {
	pathParts := append([]string{"services", service}, parts...)
	return fieldPath(pathParts...)
}

func dependencyField(service string, index int, parts ...string) string {
	dep := fmt.Sprintf("dependsOn[%d]", index)
	pathParts := append([]string{dep}, parts...)
	return serviceField(service, pathParts...)
}

func probeField(service string, parts ...string) string {
	pathParts := append([]string{"health"}, parts...)
	return serviceField(service, pathParts...)
}

// ServicesSorted returns service names sorted alphabetically.
func (s *Stack) ServicesSorted() []string {
	out := make([]string, 0, len(s.Services))
	for name := range s.Services {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

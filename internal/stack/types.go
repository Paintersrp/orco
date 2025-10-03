package stack

import (
	"fmt"
	"time"
)

// Duration wraps time.Duration for YAML unmarshalling.
type Duration struct {
	time.Duration
}

// UnmarshalText parses a textual duration, accepting empty strings.
func (d *Duration) UnmarshalText(text []byte) error {
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

// StackFile represents the contents of stack.yaml.
type StackFile struct {
	Version  string     `yaml:"version"`
	Stack    StackMeta  `yaml:"stack"`
	Defaults Defaults   `yaml:"defaults"`
	Services ServiceMap `yaml:"services"`
}

// StackMeta contains metadata about the stack itself.
type StackMeta struct {
	Name    string `yaml:"name"`
	Workdir string `yaml:"workdir"`
}

// Defaults defines global defaults applied to services.
type Defaults struct {
	RestartPolicy *RestartPolicy `yaml:"restartPolicy"`
	Health        *Health        `yaml:"health"`
}

// ServiceMap is a map keyed by service name.
type ServiceMap map[string]*Service

// Service describes an individual runnable unit.
type Service struct {
	Image         string            `yaml:"image"`
	Runtime       string            `yaml:"runtime"`
	Command       []string          `yaml:"command"`
	Env           map[string]string `yaml:"env"`
	EnvFromFile   string            `yaml:"envFromFile"`
	Ports         []string          `yaml:"ports"`
	DependsOn     []Dependency      `yaml:"dependsOn"`
	Health        *Health           `yaml:"health"`
	Update        *UpdatePolicy     `yaml:"update"`
	Replicas      int               `yaml:"replicas"`
	RestartPolicy *RestartPolicy    `yaml:"restartPolicy"`
}

// Dependency defines an edge in the service DAG.
type Dependency struct {
	Target  string   `yaml:"target"`
	Require string   `yaml:"require"`
	Timeout Duration `yaml:"timeout"`
}

// Health describes the health checking configuration.
type Health struct {
	GracePeriod      Duration      `yaml:"gracePeriod"`
	Interval         Duration      `yaml:"interval"`
	Timeout          Duration      `yaml:"timeout"`
	FailureThreshold int           `yaml:"failureThreshold"`
	SuccessThreshold int           `yaml:"successThreshold"`
	HTTP             *HTTPProbe    `yaml:"http"`
	TCP              *TCPProbe     `yaml:"tcp"`
	Command          *CommandProbe `yaml:"cmd"`
}

// HTTPProbe defines readiness detection via HTTP.
type HTTPProbe struct {
	URL          string `yaml:"url"`
	ExpectStatus []int  `yaml:"expectStatus"`
}

// TCPProbe defines readiness detection via TCP dial.
type TCPProbe struct {
	Address string `yaml:"address"`
}

// CommandProbe executes a command as a readiness probe.
type CommandProbe struct {
	Command []string `yaml:"command"`
	Timeout Duration `yaml:"timeout"`
}

// UpdatePolicy controls rolling update parameters.
type UpdatePolicy struct {
	Strategy       string `yaml:"strategy"`
	MaxUnavailable int    `yaml:"maxUnavailable"`
	MaxSurge       int    `yaml:"maxSurge"`
}

// RestartPolicy controls restart behavior.
type RestartPolicy struct {
	MaxRetries int      `yaml:"maxRetries"`
	Backoff    *Backoff `yaml:"backoff"`
}

// Backoff describes exponential backoff configuration.
type Backoff struct {
	Min    Duration `yaml:"min"`
	Max    Duration `yaml:"max"`
	Factor float64  `yaml:"factor"`
}

// Clone creates a deep copy of the service.
func (s *Service) Clone() *Service {
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
	if s.DependsOn != nil {
		cp.DependsOn = append([]Dependency(nil), s.DependsOn...)
	}
	if s.Health != nil {
		cp.Health = s.Health.Clone()
	}
	if s.Update != nil {
		cp.Update = &UpdatePolicy{
			Strategy:       s.Update.Strategy,
			MaxUnavailable: s.Update.MaxUnavailable,
			MaxSurge:       s.Update.MaxSurge,
		}
	}
	if s.RestartPolicy != nil {
		cp.RestartPolicy = s.RestartPolicy.Clone()
	}
	return &cp
}

// Clone creates a deep copy of the health configuration.
func (h *Health) Clone() *Health {
	if h == nil {
		return nil
	}
	cp := *h
	if h.HTTP != nil {
		httpCopy := *h.HTTP
		cp.HTTP = &httpCopy
		if h.HTTP.ExpectStatus != nil {
			cp.HTTP.ExpectStatus = append([]int(nil), h.HTTP.ExpectStatus...)
		}
	}
	if h.TCP != nil {
		tcpCopy := *h.TCP
		cp.TCP = &tcpCopy
	}
	if h.Command != nil {
		cmdCopy := *h.Command
		if h.Command.Command != nil {
			cmdCopy.Command = append([]string(nil), h.Command.Command...)
		}
		cp.Command = &cmdCopy
	}
	return &cp
}

// ApplyDefaults merges non-zero default thresholds and durations into h.
func (h *Health) ApplyDefaults(def *Health) {
	if h == nil || def == nil {
		return
	}
	if h.GracePeriod.Duration == 0 && def.GracePeriod.Duration != 0 {
		h.GracePeriod = def.GracePeriod
	}
	if h.Interval.Duration == 0 && def.Interval.Duration != 0 {
		h.Interval = def.Interval
	}
	if h.Timeout.Duration == 0 && def.Timeout.Duration != 0 {
		h.Timeout = def.Timeout
	}
	if h.FailureThreshold == 0 && def.FailureThreshold != 0 {
		h.FailureThreshold = def.FailureThreshold
	}
	if h.SuccessThreshold == 0 && def.SuccessThreshold != 0 {
		h.SuccessThreshold = def.SuccessThreshold
	}
}

// Clone creates a deep copy of the restart policy.
func (r *RestartPolicy) Clone() *RestartPolicy {
	if r == nil {
		return nil
	}
	cp := *r
	if r.Backoff != nil {
		backoffCopy := *r.Backoff
		cp.Backoff = &backoffCopy
	}
	return &cp
}

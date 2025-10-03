package stack

import (
	"fmt"
	"io"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Parse reads a stack definition from YAML.
func Parse(r io.Reader) (*StackFile, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	var doc StackFile
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode stack: %w", err)
	}
	if err := doc.ApplyDefaults(); err != nil {
		return nil, err
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// ApplyDefaults merges stack defaults onto services.
func (s *StackFile) ApplyDefaults() error {
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
		if svc.RestartPolicy == nil && s.Defaults.RestartPolicy != nil {
			svc.RestartPolicy = s.Defaults.RestartPolicy.Clone()
		}
		if svc.Health == nil && s.Defaults.Health != nil {
			svc.Health = s.Defaults.Health.Clone()
		}
	}
	return nil
}

// Validate enforces schema invariants.
func (s *StackFile) Validate() error {
	if s.Version == "" {
		return fmt.Errorf("version is required")
	}
	if len(s.Services) == 0 {
		return fmt.Errorf("at least one service must be defined")
	}
	if s.Stack.Name == "" {
		return fmt.Errorf("stack.name is required")
	}
	for name, svc := range s.Services {
		if svc.Runtime == "" {
			return fmt.Errorf("service %s missing runtime", name)
		}
		if svc.Runtime != "docker" && svc.Runtime != "process" {
			return fmt.Errorf("service %s has unsupported runtime %q", name, svc.Runtime)
		}
		if svc.Health != nil {
			if err := validateHealth(name, svc.Health); err != nil {
				return err
			}
		}
		if svc.Replicas < 1 {
			return fmt.Errorf("service %s must have at least one replica", name)
		}
		if svc.RestartPolicy != nil && svc.RestartPolicy.Backoff != nil {
			if svc.RestartPolicy.Backoff.Factor == 0 {
				return fmt.Errorf("service %s backoff factor must be non-zero", name)
			}
		}
		for i, dep := range svc.DependsOn {
			if dep.Target == "" {
				return fmt.Errorf("service %s dependency %d missing target", name, i)
			}
			switch dep.Require {
			case "", "ready", "started", "exists":
			default:
				return fmt.Errorf("service %s dependency %s has invalid require=%s", name, dep.Target, dep.Require)
			}
			if _, ok := s.Services[dep.Target]; !ok {
				return fmt.Errorf("service %s dependency %s references unknown service", name, dep.Target)
			}
		}
	}
	return nil
}

func validateHealth(name string, h *Health) error {
	probes := 0
	if h.HTTP != nil {
		probes++
	}
	if h.TCP != nil {
		probes++
	}
	if h.Command != nil {
		probes++
	}
	if probes > 1 {
		return fmt.Errorf("service %s defines multiple probe types; only one supported in v0.1", name)
	}
	if probes == 0 {
		return fmt.Errorf("service %s health block present but no probe configured", name)
	}
	if h.FailureThreshold == 0 {
		h.FailureThreshold = 3
	}
	if h.Interval.Duration == 0 {
		h.Interval.Duration = 2 * time.Second
	}
	if h.Timeout.Duration == 0 {
		h.Timeout.Duration = time.Second
	}
	if h.GracePeriod.Duration == 0 {
		h.GracePeriod.Duration = 5 * time.Second
	}
	if h.SuccessThreshold == 0 {
		h.SuccessThreshold = 1
	}
	return nil
}

// ServicesSorted returns service names sorted alphabetically.
func (s *StackFile) ServicesSorted() []string {
	out := make([]string, 0, len(s.Services))
	for name := range s.Services {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

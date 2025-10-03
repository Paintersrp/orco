package stack

import (
	"fmt"
	"io"
	"sort"
	"strings"
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
func (s *StackFile) Validate() error {
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
		if svc.Health != nil {
			if err := validateHealth(name, svc.Health); err != nil {
				return err
			}
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
	}
	return nil
}

func validateHealth(name string, h *Health) error {
	probes := 0
	if h.HTTP != nil {
		probes++
		if h.HTTP.URL == "" {
			return fmt.Errorf("%s: is required", healthField(name, "http", "url"))
		}
	}
	if h.TCP != nil {
		probes++
		if h.TCP.Address == "" {
			return fmt.Errorf("%s: is required", healthField(name, "tcp", "address"))
		}
	}
	if h.Command != nil {
		probes++
		if len(h.Command.Command) == 0 {
			return fmt.Errorf("%s: must contain at least one entry", healthField(name, "cmd", "command"))
		}
	}
	if probes > 1 {
		return fmt.Errorf("%s: multiple probe types configured; only one is supported in v0.1", healthField(name))
	}
	if probes == 0 {
		return fmt.Errorf("%s: probe configuration is required", healthField(name))
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
	if h.Command != nil && h.Command.Timeout.Duration == 0 {
		h.Command.Timeout = h.Timeout
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

func healthField(service string, parts ...string) string {
	pathParts := append([]string{"health"}, parts...)
	return serviceField(service, pathParts...)
}

package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/go-connections/nat"
)

type portClaim struct {
	services map[string]struct{}
}

func validatePortCollisions(s *Stack) error {
	if len(s.Services) == 0 {
		return nil
	}
	claimed := map[string]*portClaim{}
	for serviceName, svc := range s.Services {
		if svc == nil {
			continue
		}
		for idx, spec := range svc.Ports {
			mappings, err := nat.ParsePortSpec(spec)
			if err != nil {
				return fmt.Errorf("%s: invalid port mapping %q: %w", serviceField(serviceName, fmt.Sprintf("ports[%d]", idx)), spec, err)
			}
			for _, mapping := range mappings {
				hostPortSpec := strings.TrimSpace(mapping.Binding.HostPort)
				if hostPortSpec == "" {
					continue
				}
				hostIP := normalizeHostIP(mapping.Binding.HostIP)
				start, end, err := nat.ParsePortRange(hostPortSpec)
				if err != nil {
					return fmt.Errorf("%s: invalid host port %q", serviceField(serviceName, fmt.Sprintf("ports[%d]", idx)), hostPortSpec)
				}
				startPort := int(start)
				endPort := int(end)
				for port := startPort; port <= endPort; port++ {
					specificKey := hostPortKey(hostIP, port)
					wildcardKey := hostPortKey("0.0.0.0", port)

					var conflictServices map[string]struct{}
					for _, key := range []string{specificKey, wildcardKey} {
						if claim := claimed[key]; claim != nil {
							if conflictServices == nil {
								conflictServices = map[string]struct{}{}
							}
							for existing := range claim.services {
								conflictServices[existing] = struct{}{}
							}
						}
					}

					if len(conflictServices) > 0 {
						services := make([]string, 0, len(conflictServices)+1)
						for existing := range conflictServices {
							services = append(services, existing)
						}
						if _, seen := conflictServices[serviceName]; !seen {
							services = append(services, serviceName)
						}
						sort.Strings(services)
						next := nextAvailablePort(hostIP, port, claimed)
						if next == 0 {
							return fmt.Errorf("%s: host port %d on IP %q conflicts with service(s) %s; no additional host ports available", serviceField(serviceName, fmt.Sprintf("ports[%d]", idx)), port, hostIP, strings.Join(services, ", "))
						}
						return fmt.Errorf("%s: host port %d on IP %q conflicts with service(s) %s; next available port is %d", serviceField(serviceName, fmt.Sprintf("ports[%d]", idx)), port, hostIP, strings.Join(services, ", "), next)
					}

					claim := claimed[specificKey]
					if claim == nil {
						claim = &portClaim{services: map[string]struct{}{}}
						claimed[specificKey] = claim
					}
					if hostIP == "0.0.0.0" {
						claimed[wildcardKey] = claim
					}
					claim.services[serviceName] = struct{}{}
				}
			}
		}
	}
	return nil
}

func nextAvailablePort(hostIP string, start int, claimed map[string]*portClaim) int {
	for candidate := start + 1; candidate <= 65535; candidate++ {
		specific := claimed[hostPortKey(hostIP, candidate)]
		wildcard := claimed[hostPortKey("0.0.0.0", candidate)]
		if (specific == nil || len(specific.services) == 0) && (wildcard == nil || len(wildcard.services) == 0) {
			return candidate
		}
	}
	return 0
}

func hostPortKey(hostIP string, port int) string {
	return fmt.Sprintf("%s:%d", hostIP, port)
}

func normalizeHostIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" || ip == "0.0.0.0" {
		return "0.0.0.0"
	}
	return ip
}

func collectVolumeConflicts(s *Stack) []string {
	if len(s.Services) == 0 {
		return nil
	}

	writable := make(map[string]map[string]struct{})
	for serviceName, svc := range s.Services {
		if svc == nil || len(svc.Volumes) == 0 {
			continue
		}
		seen := make(map[string]struct{})
		for _, spec := range svc.Volumes {
			host, _, mode, err := splitVolumeSpec(spec)
			if err != nil {
				continue
			}
			host = filepath.Clean(host)
			if host == "" {
				continue
			}
			if mode == "" {
				mode = "rw"
			}
			if !isWritableVolumeMode(mode) {
				continue
			}
			if _, dup := seen[host]; dup {
				continue
			}
			seen[host] = struct{}{}
			serviceSet := writable[host]
			if serviceSet == nil {
				serviceSet = make(map[string]struct{})
				writable[host] = serviceSet
			}
			serviceSet[serviceName] = struct{}{}
		}
	}

	var warnings []string
	for host, services := range writable {
		if len(services) < 2 {
			continue
		}
		names := make([]string, 0, len(services))
		for name := range services {
			names = append(names, name)
		}
		sort.Strings(names)
		warnings = append(warnings, fmt.Sprintf("services %s share writable host path %q; consider mounting it as read-only (mode \"ro\") or using distinct host directories", strings.Join(names, ", "), host))
	}
	sort.Strings(warnings)
	return warnings
}

func isWritableVolumeMode(mode string) bool {
	mode = strings.ToLower(mode)
	parts := strings.Split(mode, ",")
	for _, part := range parts {
		if strings.TrimSpace(part) == "ro" {
			return false
		}
	}
	return true
}

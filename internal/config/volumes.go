package config

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

func splitVolumeSpec(spec string) (host string, container string, mode string, err error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return "", "", "", fmt.Errorf("volume specification is empty")
	}
	first := strings.Index(trimmed, ":")
	if first == -1 {
		return "", "", "", fmt.Errorf("invalid volume specification %q: expected format hostPath:containerPath[:mode]", spec)
	}
	host = strings.TrimSpace(trimmed[:first])
	if host == "" {
		return "", "", "", fmt.Errorf("invalid volume specification %q: host path is required", spec)
	}
	remainder := trimmed[first+1:]
	if remainder == "" {
		return "", "", "", fmt.Errorf("invalid volume specification %q: container path is required", spec)
	}
	second := strings.Index(remainder, ":")
	if second == -1 {
		container = strings.TrimSpace(remainder)
		if container == "" {
			return "", "", "", fmt.Errorf("invalid volume specification %q: container path is required", spec)
		}
		return host, container, "", nil
	}
	container = strings.TrimSpace(remainder[:second])
	if container == "" {
		return "", "", "", fmt.Errorf("invalid volume specification %q: container path is required", spec)
	}
	mode = strings.TrimSpace(remainder[second+1:])
	if mode == "" {
		return "", "", "", fmt.Errorf("invalid volume specification %q: mode is empty", spec)
	}
	if strings.Contains(mode, ":") {
		return "", "", "", fmt.Errorf("invalid volume specification %q: unexpected ':' in mode", spec)
	}
	return host, container, mode, nil
}

func validateVolumeSpec(spec string) error {
	host, container, _, err := splitVolumeSpec(spec)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(host) {
		return fmt.Errorf("host path %q must be absolute", host)
	}
	if !path.IsAbs(container) {
		return fmt.Errorf("container path %q must be absolute", container)
	}
	return nil
}

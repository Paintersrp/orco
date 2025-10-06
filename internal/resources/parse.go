package resources

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	units "github.com/docker/go-units"
)

const NanoCPUs = 1_000_000_000

// ParseCPU converts a textual CPU quantity into Docker nanocpu units.
// Supported formats include fractional core counts (e.g. "0.5") and
// millicores using the Kubernetes-style suffix (e.g. "500m").
func ParseCPU(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	var cores float64
	var err error
	if strings.HasSuffix(trimmed, "m") || strings.HasSuffix(trimmed, "M") {
		milli := strings.TrimSpace(trimmed[:len(trimmed)-1])
		if milli == "" {
			return 0, fmt.Errorf("invalid cpu quantity %q", value)
		}
		var milliVal float64
		milliVal, err = strconv.ParseFloat(milli, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid cpu quantity %q: %w", value, err)
		}
		cores = milliVal / 1000.0
	} else {
		cores, err = strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid cpu quantity %q: %w", value, err)
		}
	}
	if cores <= 0 {
		return 0, fmt.Errorf("invalid cpu quantity %q: must be positive", value)
	}
	nano := math.Round(cores * NanoCPUs)
	if nano < 1 {
		nano = 1
	}
	if nano > math.MaxInt64 {
		return 0, fmt.Errorf("invalid cpu quantity %q: exceeds supported range", value)
	}
	return int64(nano), nil
}

// ParseMemory converts textual memory limits like "512Mi" into bytes.
func ParseMemory(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasSuffix(lower, "kib"), strings.HasSuffix(lower, "mib"), strings.HasSuffix(lower, "gib"), strings.HasSuffix(lower, "tib"), strings.HasSuffix(lower, "pib"), strings.HasSuffix(lower, "eib"):
		// already in binary units understood by go-units
	case strings.HasSuffix(lower, "ki"), strings.HasSuffix(lower, "mi"), strings.HasSuffix(lower, "gi"), strings.HasSuffix(lower, "ti"), strings.HasSuffix(lower, "pi"), strings.HasSuffix(lower, "ei"):
		trimmed += "B"
	}
	bytes, err := units.RAMInBytes(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid memory quantity %q: %w", value, err)
	}
	if bytes <= 0 {
		return 0, fmt.Errorf("invalid memory quantity %q: must be positive", value)
	}
	return bytes, nil
}

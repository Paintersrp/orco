package containerutil

import (
	"fmt"
	"sort"
	"strings"

	"github.com/docker/docker/volume/mounts"
	"github.com/docker/go-connections/nat"

	"github.com/Paintersrp/orco/internal/resources"
	"github.com/Paintersrp/orco/internal/runtime"
)

type PortMapping struct {
	Port     nat.Port
	Bindings []nat.PortBinding
}

type Resources struct {
	NanoCPUs          int64
	CPUPeriod         int64
	CPUQuota          int64
	Memory            int64
	MemorySwap        int64
	MemoryReservation int64
}

type CommonSpec struct {
	Env         []string
	Cmd         []string
	Workdir     string
	Ports       []PortMapping
	Binds       []string
	Resources   Resources
	MemoryLimit string
}

func PrepareCommonSpec(spec runtime.StartSpec) (CommonSpec, error) {
	var common CommonSpec

	if len(spec.Env) > 0 {
		env := make([]string, 0, len(spec.Env))
		for k, v := range spec.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		sort.Strings(env)
		common.Env = env
	}

	if len(spec.Command) > 0 {
		common.Cmd = append([]string(nil), spec.Command...)
	}

	common.Workdir = spec.Workdir

	if len(spec.Ports) > 0 {
		var ports []PortMapping
		for _, portSpec := range spec.Ports {
			mappings, err := nat.ParsePortSpec(portSpec)
			if err != nil {
				return CommonSpec{}, fmt.Errorf("parse port %q: %w", portSpec, err)
			}
			for _, mapping := range mappings {
				bindings := []nat.PortBinding{mapping.Binding}
				ports = append(ports, PortMapping{
					Port:     mapping.Port,
					Bindings: bindings,
				})
			}
		}
		common.Ports = ports
	}

	if len(spec.Volumes) > 0 {
		parser := mounts.NewParser()
		binds := make([]string, 0, len(spec.Volumes))
		for _, volume := range spec.Volumes {
			bindSpec := volume.Source + ":" + volume.Target
			if volume.Mode != "" {
				bindSpec += ":" + volume.Mode
			}
			if _, err := parser.ParseMountRaw(bindSpec, ""); err != nil {
				return CommonSpec{}, fmt.Errorf("parse volume %q: %w", bindSpec, err)
			}
			binds = append(binds, bindSpec)
		}
		common.Binds = binds
	}

	if spec.Resources != nil {
		var limits Resources
		if strings.TrimSpace(spec.Resources.CPU) != "" {
			nano, err := resources.ParseCPU(spec.Resources.CPU)
			if err != nil {
				return CommonSpec{}, fmt.Errorf("parse cpu: %w", err)
			}
			if nano > 0 {
				const cpuPeriod = 100000
				limits.NanoCPUs = nano
				limits.CPUPeriod = cpuPeriod
				quota := (nano*cpuPeriod + resources.NanoCPUs/2) / resources.NanoCPUs
				if quota < 1 {
					quota = 1
				}
				limits.CPUQuota = quota
			}
		}
		if mem := strings.TrimSpace(spec.Resources.Memory); mem != "" {
			bytes, err := resources.ParseMemory(mem)
			if err != nil {
				return CommonSpec{}, fmt.Errorf("parse memory: %w", err)
			}
			limits.Memory = bytes
			limits.MemorySwap = bytes
			common.MemoryLimit = mem
		}
		if strings.TrimSpace(spec.Resources.MemoryReservation) != "" {
			bytes, err := resources.ParseMemory(spec.Resources.MemoryReservation)
			if err != nil {
				return CommonSpec{}, fmt.Errorf("parse memory reservation: %w", err)
			}
			limits.MemoryReservation = bytes
		}
		common.Resources = limits
	}

	if common.MemoryLimit == "" && spec.Resources != nil {
		common.MemoryLimit = spec.Resources.Memory
	}

	return common, nil
}

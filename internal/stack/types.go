package stack

import "github.com/Paintersrp/orco/internal/config"

type (
	Duration      = config.Duration
	StackFile     = config.Stack
	StackMeta     = config.StackMeta
	Defaults      = config.Defaults
	Proxy         = config.ProxySpec
	ProxyRoute    = config.ProxyRoute
	ProxyAssets   = config.ProxyAssetSpec
	Service       = config.ServiceSpec
	ServiceMap    = map[string]*Service
	Dependency    = config.DepEdge
	Health        = config.ProbeSpec
	HTTPProbe     = config.HTTPProbeSpec
	TCPProbe      = config.TCPProbeSpec
	CommandProbe  = config.CommandProbe
	LogProbe      = config.LogProbeSpec
	UpdatePolicy  = config.UpdateStrategy
	RestartPolicy = config.RestartPolicy
	Backoff       = config.BackoffSpec
	Resources     = config.Resources
)

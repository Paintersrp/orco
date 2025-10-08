package metrics

import (
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	registry = prometheus.NewRegistry()

	serviceReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "orco",
		Name:      "service_ready",
		Help:      "Readiness state of services (1=ready, 0=not ready).",
	}, []string{"service"})

	serviceRestarts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "orco",
		Name:      "service_restarts_total",
		Help:      "Total number of restarts initiated for each service.",
	}, []string{"service"})

	probeLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "orco",
		Name:      "probe_latency_seconds",
		Help:      "Latency of readiness probe executions in seconds.",
	}, []string{"service"})

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "orco",
		Name:      "build_info",
		Help:      "Build metadata for the running orco binary.",
	}, []string{"go_version", "vcs", "vcs_revision", "vcs_time", "vcs_modified"})

	buildInfoOnce sync.Once
)

func init() {
	registry.MustRegister(serviceReady, serviceRestarts, probeLatency, buildInfo)
}

// Registry returns the Prometheus registry containing all orco metrics.
func Registry() *prometheus.Registry {
	return registry
}

// SetServiceReady records the readiness state for the provided service.
func SetServiceReady(service string, ready bool) {
	if service == "" {
		return
	}
	value := 0.0
	if ready {
		value = 1.0
	}
	serviceReady.WithLabelValues(service).Set(value)
}

// AddServiceRestarts increments the restart counter for a service.
func AddServiceRestarts(service string, n int) {
	if service == "" || n <= 0 {
		return
	}
	serviceRestarts.WithLabelValues(service).Add(float64(n))
}

// IncrementServiceRestart increments the restart counter by one for a service.
func IncrementServiceRestart(service string) {
	AddServiceRestarts(service, 1)
}

// ObserveProbeLatency records the latency of a readiness probe.
func ObserveProbeLatency(service string, d time.Duration) {
	label := service
	if label == "" {
		label = "unknown"
	}
	probeLatency.WithLabelValues(label).Observe(d.Seconds())
}

// EmitBuildInfo publishes build metadata about the running binary.
func EmitBuildInfo() {
	buildInfoOnce.Do(func() {
		labels := prometheus.Labels{
			"go_version":   runtime.Version(),
			"vcs":          "",
			"vcs_revision": "",
			"vcs_time":     "",
			"vcs_modified": "",
		}
		if info, ok := debug.ReadBuildInfo(); ok {
			if info.GoVersion != "" {
				labels["go_version"] = info.GoVersion
			}
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs":
					labels["vcs"] = setting.Value
				case "vcs.revision":
					labels["vcs_revision"] = setting.Value
				case "vcs.time":
					labels["vcs_time"] = setting.Value
				case "vcs.modified":
					labels["vcs_modified"] = setting.Value
				}
			}
		}
		buildInfo.With(labels).Set(1)
	})
}

// ResetService clears the readiness gauge for a service.
func ResetService(service string) {
	if service == "" {
		return
	}
	serviceReady.DeleteLabelValues(service)
	serviceRestarts.DeleteLabelValues(service)
	probeLatency.DeleteLabelValues(service)
}

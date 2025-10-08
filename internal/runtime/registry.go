package runtime

import "sync"

// Factory constructs a runtime instance.
type Factory func() Runtime

type factoryEntry struct {
	name    string
	factory Factory
}

var (
	registryMu       sync.RWMutex
	builtinFactories []factoryEntry
)

// Register associates the provided factory with the runtime name. When multiple
// factories register the same name the most recent registration wins.
func Register(name string, factory Factory) {
	if name == "" {
		panic("runtime.Register: name must not be empty")
	}
	if factory == nil {
		panic("runtime.Register: factory must not be nil")
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	for i, entry := range builtinFactories {
		if entry.name == name {
			builtinFactories[i].factory = factory
			return
		}
	}

	builtinFactories = append(builtinFactories, factoryEntry{name: name, factory: factory})
}

// NewRegistry constructs the default runtime registry containing all registered
// runtime adapters.
func NewRegistry() Registry {
	registryMu.RLock()
	defer registryMu.RUnlock()

	reg := make(Registry, len(builtinFactories))
	for _, entry := range builtinFactories {
		reg[entry.name] = entry.factory()
	}
	return reg
}

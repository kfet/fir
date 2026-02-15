package extension

import "sync"

var (
	registryMu sync.Mutex
	factories  []RegisteredFactory
)

// RegisteredFactory holds a registered extension factory.
type RegisteredFactory struct {
	Name    string
	Factory Factory
}

// Register adds an extension factory to the global registry.
// Typically called from an extension package's init() function.
func Register(name string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	factories = append(factories, RegisteredFactory{Name: name, Factory: factory})
}

// RegisteredFactories returns a copy of all registered extension factories.
func RegisteredFactories() []RegisteredFactory {
	registryMu.Lock()
	defer registryMu.Unlock()
	result := make([]RegisteredFactory, len(factories))
	copy(result, factories)
	return result
}

// RegisteredNames returns the names of all registered extension factories.
func RegisteredNames() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, len(factories))
	for i, f := range factories {
		names[i] = f.Name
	}
	return names
}

// ClearRegistry removes all registered factories. Used for testing.
func ClearRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	factories = nil
}

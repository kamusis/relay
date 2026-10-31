package registry

import (
	"fmt"
	"sync"

	"github.com/kamusis/relay/pkg/adapter"
)

// Registry manages the set of available Agent Adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters map[adapter.AgentType]adapter.Adapter
}

// New creates an initialized, empty Registry.
func New() *Registry {
	return &Registry{
		adapters: make(map[adapter.AgentType]adapter.Adapter),
	}
}

// Default is the global singleton registry instance.
var Default = New()

// Register adds an adapter to the registry. If an adapter of the same type is already registered, it is overwritten.
func (r *Registry) Register(a adapter.Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.Type()] = a
}

// Get retrieves an adapter by its AgentType.
func (r *Registry) Get(t adapter.AgentType) (adapter.Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[t]
	if !ok {
		return nil, fmt.Errorf("no adapter registered for agent type %q", t)
	}
	return a, nil
}

// List returns a slice of all registered adapters.
func (r *Registry) List() []adapter.Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]adapter.Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		list = append(list, a)
	}
	return list
}

// Register registers an adapter into the Default registry.
func Register(a adapter.Adapter) {
	Default.Register(a)
}

// Get retrieves an adapter from the Default registry.
func Get(t adapter.AgentType) (adapter.Adapter, error) {
	return Default.Get(t)
}

// List returns all adapters in the Default registry.
func List() []adapter.Adapter {
	return Default.List()
}

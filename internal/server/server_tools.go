package server

import (
	"fmt"
	"sort"
	"sync"

	"murmur/internal/bus"
	"murmur/internal/tools"
)

// ToolRegistry holds server-side tools that execute locally without routing
// through the bus to a client. These tools are merged with client-provided
// tools when assembling the LLM's tool list, and are checked first during
// tool call dispatch (server tools take priority over client tools with the
// same name).
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]tools.Tool
}

// NewToolRegistry creates an empty server-side tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]tools.Tool),
	}
}

// Register adds a tool to the registry. Returns an error if a tool with the
// same name is already registered.
func (r *ToolRegistry) Register(t tools.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("register: tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// Get returns the tool with the given name and true, or a zero Tool and false
// if no tool with that name is registered.
func (r *ToolRegistry) Get(name string) (tools.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	return t, ok
}

// AllToolDefs returns all registered server tools as bus.ToolDef values,
// sorted by name for deterministic ordering. Suitable for merging with
// client tool definitions before sending to the LLM.
func (r *ToolRegistry) AllToolDefs() []bus.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	toolSlice := make([]tools.Tool, len(names))
	for i, name := range names {
		toolSlice[i] = r.tools[name]
	}
	return tools.ToBusToolDefs(toolSlice)
}

// Unregister removes a tool from the registry by name. It is not an error
// if the tool does not exist.
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// HasTool returns true if a tool with the given name is registered.
func (r *ToolRegistry) HasTool(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// Names returns a sorted list of all registered tool names.
func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

package server

import (
	"fmt"

	"murmur/internal/tools"
)

// MemoryAdapter adapts the server's Memory type to the tools.MemoryReader
// interface. It converts llm.Message values to tools.MemoryMessage values,
// decoupling the tools package from the llm package.
type MemoryAdapter struct {
	memory *Memory
}

// NewMemoryAdapter creates a MemoryAdapter wrapping the given Memory instance.
func NewMemoryAdapter(m *Memory) *MemoryAdapter {
	return &MemoryAdapter{memory: m}
}

// GetHistory retrieves the last limit messages for a channel, converting
// each llm.Message to a tools.MemoryMessage (Role + Content only).
func (a *MemoryAdapter) GetHistory(channel string, limit int) ([]tools.MemoryMessage, error) {
	msgs, err := a.memory.GetHistory(channel, limit)
	if err != nil {
		return nil, fmt.Errorf("MemoryAdapter.GetHistory: %w", err)
	}

	result := make([]tools.MemoryMessage, len(msgs))
	for i, m := range msgs {
		result[i] = tools.MemoryMessage{
			Role:    m.Role,
			Content: m.Content,
		}
	}
	return result, nil
}

// GetHistoryCount returns the number of messages stored for a channel.
func (a *MemoryAdapter) GetHistoryCount(channel string) (int, error) {
	return a.memory.GetHistoryCount(channel)
}

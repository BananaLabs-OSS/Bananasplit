package queue

import (
	"sync"
	"time"
)

// QueueEntry represents a player waiting in queue
type QueueEntry struct {
	UUID        string    `json:"uuid"`
	LobbyServer string    `json:"lobbyServer"`
	JoinedAt    time.Time `json:"joinedAt"`
}

// Queue holds players waiting for a specific game mode
type Queue struct {
	entries []QueueEntry
}

// Manager manages all queues by game mode
type Manager struct {
	mu     sync.RWMutex
	queues map[string]*Queue // key = mode (e.g., "skywars")
}

// NewManager creates a new queue manager
func NewManager() (*Manager, error) {
	return &Manager{
		queues: make(map[string]*Queue),
	}, nil
}

// Join adds a player to a queue
func (m *Manager) Join(mode string, entry QueueEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.queues[mode] == nil {
		m.queues[mode] = &Queue{}
	}

	entry.JoinedAt = time.Now()
	m.queues[mode].entries = append(m.queues[mode].entries, entry)
}

// Leave removes a player from a queue
func (m *Manager) Leave(mode string, uuid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	q := m.queues[mode]
	if q == nil {
		return false
	}

	for i, entry := range q.entries {
		if entry.UUID == uuid {
			q.entries = append(q.entries[:i], q.entries[i+1:]...)
			return true
		}
	}
	return false
}

// Pop removes and returns n players from the front of a queue (FIFO)
func (m *Manager) Pop(mode string, n int) []QueueEntry {
	m.mu.Lock()
	defer m.mu.Unlock()

	q := m.queues[mode]
	if q == nil || len(q.entries) < n {
		return nil
	}

	entries := make([]QueueEntry, n)
	copy(entries, q.entries[:n])
	q.entries = q.entries[n:]
	return entries
}

// Size returns the number of players in a queue
func (m *Manager) Size(mode string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	q := m.queues[mode]
	if q == nil {
		return 0
	}
	return len(q.entries)
}

// Peek returns players without removing them
func (m *Manager) Peek(mode string, n int) []QueueEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	q := m.queues[mode]
	if q == nil {
		return nil
	}

	if n > len(q.entries) {
		n = len(q.entries)
	}

	entries := make([]QueueEntry, n)
	copy(entries, q.entries[:n])
	return entries
}

// Modes returns all active queue modes
func (m *Manager) Modes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	modes := make([]string, 0, len(m.queues))
	for mode := range m.queues {
		modes = append(modes, mode)
	}
	return modes
}

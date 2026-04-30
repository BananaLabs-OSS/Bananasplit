package main

import (
	"fmt"
	"sync"
	"time"
)

// QueueManager owns one FIFO queue per mode. Cleanup of expired
// entries is driven from the matcher tick (no background goroutine
// needed — cell runtime is step-based, not concurrent).
type QueueManager struct {
	mu      sync.Mutex
	queues  map[string][]QueueEntry
	timeout time.Duration
}

func NewQueueManager(timeout time.Duration) *QueueManager {
	return &QueueManager{
		queues:  map[string][]QueueEntry{},
		timeout: timeout,
	}
}

// Join appends an entry to the named mode's queue.
//
// Native uses time.Now() (local zone) for JoinedAt. The field is
// never serialized back out of the service — the matcher reads it
// only via now.Sub(joinedAt) to compute wait duration — so UTC vs
// local has no observable effect. Match native exactly anyway so a
// parity test that snapshots in-memory state does not diff.
func (m *QueueManager) Join(mode string, entry QueueEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry.JoinedAt = time.Now()
	m.queues[mode] = append(m.queues[mode], entry)
}

// Leave removes the first entry matching uuid. Returns true if an
// entry was removed.
func (m *QueueManager) Leave(mode, uuid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.queues[mode]
	for i, e := range entries {
		if e.UUID == uuid {
			m.queues[mode] = append(entries[:i], entries[i+1:]...)
			return true
		}
	}
	return false
}

// Size reports how many players are currently queued for mode.
func (m *QueueManager) Size(mode string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queues[mode])
}

// Pop removes n players from the front of mode's queue. Returns nil
// when fewer than n are queued.
func (m *QueueManager) Pop(mode string, n int) []QueueEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.queues[mode]
	if len(entries) < n {
		return nil
	}
	popped := make([]QueueEntry, n)
	copy(popped, entries[:n])
	m.queues[mode] = entries[n:]
	return popped
}

// Modes returns all currently-known mode names.
func (m *QueueManager) Modes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.queues))
	for mode := range m.queues {
		out = append(out, mode)
	}
	return out
}

// Cleanup drops entries older than the configured timeout. Called
// periodically from the matcher tick.
func (m *QueueManager) Cleanup() {
	if m.timeout <= 0 {
		return
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for mode, entries := range m.queues {
		kept := make([]QueueEntry, 0, len(entries))
		for _, e := range entries {
			if now.Sub(e.JoinedAt) < m.timeout {
				kept = append(kept, e)
			} else {
				fmt.Printf("[Queue] Timeout: %s removed from %s (waited %s)\n", e.UUID, mode, now.Sub(e.JoinedAt))
			}
		}
		m.queues[mode] = kept
	}
}

// PlayerRegistry keeps the UUID↔IP↔server mapping for every connected
// player. Look-ups are used by the matcher to set Peel routes and by
// the HTTP API for DELETE /players/:uuid.
type PlayerRegistry struct {
	mu     sync.RWMutex
	byUUID map[string]*Player
	byIP   map[string]*Player
}

func NewPlayerRegistry() *PlayerRegistry {
	return &PlayerRegistry{
		byUUID: map[string]*Player{},
		byIP:   map[string]*Player{},
	}
}

func (r *PlayerRegistry) Register(uuid, ip, serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := &Player{UUID: uuid, IP: ip, ServerID: serverID}
	r.byUUID[uuid] = p
	r.byIP[ip] = p
}

func (r *PlayerRegistry) GetByUUID(uuid string) (*Player, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byUUID[uuid]
	return p, ok
}

func (r *PlayerRegistry) Remove(uuid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.byUUID[uuid]; ok {
		delete(r.byIP, p.IP)
		delete(r.byUUID, uuid)
	}
}

// ReferralQueue is a per-server list of pending player referrals that
// origin game servers poll via GET /referrals.
type ReferralQueue struct {
	mu      sync.Mutex
	pending map[string][]Referral
}

func NewReferralQueue() *ReferralQueue {
	return &ReferralQueue{pending: map[string][]Referral{}}
}

func (q *ReferralQueue) Add(serverID string, ref Referral) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending[serverID] = append(q.pending[serverID], ref)
}

func (q *ReferralQueue) GetAndClear(serverID string) []Referral {
	q.mu.Lock()
	defer q.mu.Unlock()
	refs := q.pending[serverID]
	delete(q.pending, serverID)
	return refs
}

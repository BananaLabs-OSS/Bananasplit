package referrals

import (
	"sync"
)

type Referral struct {
	PlayerUUID string `json:"player_uuid"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
}

type Queue struct {
	mu      sync.Mutex
	pending map[string][]Referral
}

func NewQueue() *Queue {
	return &Queue{
		pending: make(map[string][]Referral),
	}
}

func (q *Queue) Add(serverID string, ref Referral) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.pending[serverID] = append(q.pending[serverID], ref)
}

func (q *Queue) GetAndClear(serverID string) []Referral {
	q.mu.Lock()
	defer q.mu.Unlock()

	refs := q.pending[serverID]
	delete(q.pending, serverID)
	return refs
}

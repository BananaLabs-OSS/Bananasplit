package players

import (
	"sync"
)

type Player struct {
	UUID     string `json:"uuid"`
	IP       string `json:"ip"`
	ServerID string `json:"server_id"`
}

type Registry struct {
	mu     sync.RWMutex
	byUUID map[string]*Player
	byIP   map[string]*Player
}

func NewRegistry() *Registry {
	return &Registry{
		byUUID: make(map[string]*Player),
		byIP:   make(map[string]*Player),
	}
}

func (r *Registry) Register(uuid, ip, serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	player := &Player{UUID: uuid, IP: ip, ServerID: serverID}
	r.byUUID[uuid] = player
	r.byIP[ip] = player
}

func (r *Registry) UpdateServer(uuid, serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if player, ok := r.byUUID[uuid]; ok {
		player.ServerID = serverID
	}
}

func (r *Registry) GetByUUID(uuid string) (*Player, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	player, ok := r.byUUID[uuid]
	return player, ok
}

func (r *Registry) GetByIP(ip string) (*Player, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	player, ok := r.byIP[ip]
	return player, ok
}

func (r *Registry) Remove(uuid string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if player, ok := r.byUUID[uuid]; ok {
		delete(r.byIP, player.IP)
		delete(r.byUUID, uuid)
	}
}

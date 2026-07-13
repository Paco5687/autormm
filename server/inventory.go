package server

import (
	"sync"

	"github.com/Paco5687/autormm/internal/protocol"
)

// invRegistry correlates one-shot inventory requests with their responses.
type invRegistry struct {
	mu sync.Mutex
	m  map[string]chan *protocol.InventoryResponse
}

func newInvRegistry() *invRegistry {
	return &invRegistry{m: map[string]chan *protocol.InventoryResponse{}}
}

func (r *invRegistry) create(id string) chan *protocol.InventoryResponse {
	c := make(chan *protocol.InventoryResponse, 1)
	r.mu.Lock()
	r.m[id] = c
	r.mu.Unlock()
	return c
}

func (r *invRegistry) deliver(resp *protocol.InventoryResponse) {
	r.mu.Lock()
	c := r.m[resp.ReqID]
	delete(r.m, resp.ReqID)
	r.mu.Unlock()
	if c != nil {
		c <- resp
	}
}

func (r *invRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

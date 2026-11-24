package lsp

import "sync"

// ClientProvider returns an immutable snapshot of the currently available
// language-server clients. Callers may iterate the returned map without
// coordinating with asynchronous client initialization or restart.
type ClientProvider interface {
	Snapshot() map[string]*Client
}

// ClientRegistry owns the live set of language-server clients.
type ClientRegistry struct {
	mu      sync.RWMutex
	clients map[string]*Client
}

func NewClientRegistry() *ClientRegistry {
	return &ClientRegistry{clients: make(map[string]*Client)}
}

func (r *ClientRegistry) Set(name string, client *Client) {
	if r == nil || name == "" || client == nil {
		return
	}
	r.mu.Lock()
	if r.clients == nil {
		r.clients = make(map[string]*Client)
	}
	r.clients[name] = client
	r.mu.Unlock()
}

func (r *ClientRegistry) Delete(name string) (*Client, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	client, ok := r.clients[name]
	delete(r.clients, name)
	r.mu.Unlock()
	return client, ok
}

func (r *ClientRegistry) Snapshot() map[string]*Client {
	if r == nil {
		return map[string]*Client{}
	}
	r.mu.RLock()
	clients := make(map[string]*Client, len(r.clients))
	for name, client := range r.clients {
		clients[name] = client
	}
	r.mu.RUnlock()
	return clients
}

func (r *ClientRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	length := len(r.clients)
	r.mu.RUnlock()
	return length
}

type staticClientProvider struct {
	clients map[string]*Client
}

// NewStaticClientProvider adapts legacy map-based callers while taking an
// ownership snapshot that cannot race with later map mutation.
func NewStaticClientProvider(clients map[string]*Client) ClientProvider {
	snapshot := make(map[string]*Client, len(clients))
	for name, client := range clients {
		snapshot[name] = client
	}
	return staticClientProvider{clients: snapshot}
}

func (p staticClientProvider) Snapshot() map[string]*Client {
	snapshot := make(map[string]*Client, len(p.clients))
	for name, client := range p.clients {
		snapshot[name] = client
	}
	return snapshot
}

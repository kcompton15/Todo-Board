package httpapi

import "sync"

type Hub struct {
	mu          sync.Mutex
	subscribers map[chan []byte]struct{}
}

func NewHub() *Hub {
	return &Hub{subscribers: make(map[chan []byte]struct{})}
}

func (h *Hub) Subscribe() (<-chan []byte, func()) {
	channel := make(chan []byte, 4)
	h.mu.Lock()
	h.subscribers[channel] = struct{}{}
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if _, ok := h.subscribers[channel]; ok {
			delete(h.subscribers, channel)
			close(channel)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) Publish(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for channel := range h.subscribers {
		message := append([]byte(nil), payload...)
		select {
		case channel <- message:
		default:
			delete(h.subscribers, channel)
			close(channel)
		}
	}
}

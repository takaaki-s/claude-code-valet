package notify

import (
	"sort"
	"sync"
	"time"
)

// Entry represents a single notification history entry
type Entry struct {
	SessionID   string    `json:"session_id"`
	SessionName string    `json:"session_name"`
	Type        string    `json:"type"` // "permission" | "task_complete"
	Message     string    `json:"message"`
	Timestamp   time.Time `json:"timestamp"`
	HostID      string    `json:"host_id"`
}

// History stores recent notification entries in a ring buffer
type History struct {
	mu      sync.RWMutex
	entries []Entry
	maxSize int
}

// NewHistory creates a new History with the given max size
func NewHistory(maxSize int) *History {
	return &History{
		entries: make([]Entry, 0, maxSize),
		maxSize: maxSize,
	}
}

// Add appends an entry to the history, evicting the oldest if at capacity
func (h *History) Add(entry Entry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.entries) >= h.maxSize {
		// Remove oldest entry
		h.entries = h.entries[1:]
	}
	h.entries = append(h.entries, entry)
}

// List returns a copy of all entries sorted by timestamp descending (newest first)
func (h *History) List() []Entry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make([]Entry, len(h.entries))
	copy(result, h.entries)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	return result
}

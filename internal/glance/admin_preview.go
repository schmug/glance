package glance

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type previewEntry struct {
	id         string
	sessionKey string
	app        *application
	lastAccess time.Time
	elem       *list.Element
}

type previewRegistry struct {
	mu        sync.Mutex
	capacity  int
	ttl       time.Duration
	byID      map[string]*previewEntry
	bySession map[string]map[string]struct{}
	lru       *list.List
}

func newPreviewRegistry(capacity int, ttl time.Duration) *previewRegistry {
	return &previewRegistry{
		capacity: capacity, ttl: ttl,
		byID:      make(map[string]*previewEntry),
		bySession: make(map[string]map[string]struct{}),
		lru:       list.New(),
	}
}

func (r *previewRegistry) put(sessionKey string, app *application) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if sess := r.bySession[sessionKey]; len(sess) >= r.capacity {
		for e := r.lru.Back(); e != nil; e = e.Prev() {
			entry := e.Value.(*previewEntry)
			if entry.sessionKey == sessionKey {
				r.removeLocked(entry)
				break
			}
		}
	}

	var buf [16]byte
	_, _ = rand.Read(buf[:])
	id := hex.EncodeToString(buf[:])

	entry := &previewEntry{id: id, sessionKey: sessionKey, app: app, lastAccess: time.Now()}
	entry.elem = r.lru.PushFront(entry)
	r.byID[id] = entry
	if r.bySession[sessionKey] == nil {
		r.bySession[sessionKey] = make(map[string]struct{})
	}
	r.bySession[sessionKey][id] = struct{}{}
	return id
}

func (r *previewRegistry) get(id, sessionKey string) *application {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.byID[id]
	if !ok {
		return nil
	}
	if entry.sessionKey != sessionKey {
		return nil
	}
	if time.Since(entry.lastAccess) > r.ttl {
		r.removeLocked(entry)
		return nil
	}
	entry.lastAccess = time.Now()
	r.lru.MoveToFront(entry.elem)
	return entry.app
}

func (r *previewRegistry) removeLocked(entry *previewEntry) {
	delete(r.byID, entry.id)
	if set := r.bySession[entry.sessionKey]; set != nil {
		delete(set, entry.id)
		if len(set) == 0 {
			delete(r.bySession, entry.sessionKey)
		}
	}
	r.lru.Remove(entry.elem)
}

func (r *previewRegistry) evictExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for e := r.lru.Back(); e != nil; {
		entry := e.Value.(*previewEntry)
		prev := e.Prev()
		if now.Sub(entry.lastAccess) > r.ttl {
			r.removeLocked(entry)
		}
		e = prev
	}
}

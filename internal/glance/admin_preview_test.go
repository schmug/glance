package glance

import (
	"testing"
	"time"
)

func TestPreviewRegistryStoresAndLooksUp(t *testing.T) {
	reg := newPreviewRegistry(3, 5*time.Minute)
	app := &application{}
	id := reg.put("session-A", app)
	if id == "" {
		t.Fatal("empty id")
	}
	got := reg.get(id, "session-A")
	if got != app {
		t.Fatalf("mismatch: got %v", got)
	}
}

func TestPreviewRegistryRejectsCrossSessionLookup(t *testing.T) {
	reg := newPreviewRegistry(3, 5*time.Minute)
	id := reg.put("session-A", &application{})
	if got := reg.get(id, "session-B"); got != nil {
		t.Fatal("cross-session lookup should return nil")
	}
}

func TestPreviewRegistryLRUEvicts(t *testing.T) {
	reg := newPreviewRegistry(2, 5*time.Minute)
	id1 := reg.put("s", &application{})
	id2 := reg.put("s", &application{})
	id3 := reg.put("s", &application{})
	if got := reg.get(id1, "s"); got != nil {
		t.Fatal("id1 should have been evicted (capacity 2)")
	}
	if got := reg.get(id2, "s"); got == nil {
		t.Fatal("id2 should still be present")
	}
	if got := reg.get(id3, "s"); got == nil {
		t.Fatal("id3 should still be present")
	}
}

func TestPreviewRegistryTTLEvicts(t *testing.T) {
	reg := newPreviewRegistry(3, 10*time.Millisecond)
	id := reg.put("s", &application{})
	time.Sleep(30 * time.Millisecond)
	reg.evictExpired()
	if got := reg.get(id, "s"); got != nil {
		t.Fatal("entry should have expired")
	}
}

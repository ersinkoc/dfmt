package transport

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ersinkoc/dfmt/internal/content"
)

// newStashTestHandlers spins up a Handlers with a real on-disk content store
// so dedup behavior can be observed end-to-end (PutChunkSet/PutChunk are
// mock-resistant — they enforce ID validation and persist on disk).
// Returns the Handlers and the store so tests can pass the store
// explicitly through stashContent (the global-mode signature).
func newStashTestHandlers(t *testing.T) (*Handlers, *content.Store) {
	t.Helper()
	store, err := content.NewStore(content.StoreOptions{
		Path:    filepath.Join(t.TempDir(), "content"),
		MaxSize: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewHandlers(nil, nil, nil)
	h.SetContentStore(store)
	return h, store
}

// TestStashContent_DedupReturnsSameID locks in the core dedup contract:
// re-stashing identical bytes from the same (kind, source) tuple within
// dedupTTL returns the original chunk-set ID, no new entry written.
func TestStashContent_DedupReturnsSameID(t *testing.T) {
	h, store := newStashTestHandlers(t)
	body := "the quick brown fox\n"

	id1 := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "alpha", body)
	if id1 == "" {
		t.Fatal("first stash returned empty ID")
	}
	id2 := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "beta", body)
	if id2 != id1 {
		t.Errorf("dedup must return original ID; got %q, want %q", id2, id1)
	}
	// Intent should NOT participate in dedup — the stash represents bytes,
	// and two intents asking the same question deserve the same pointer.
}

// TestStashContent_DifferentBodyDifferentID verifies dedup is keyed on body,
// not just source — changing the body bytes produces a new chunk-set.
func TestStashContent_DifferentBodyDifferentID(t *testing.T) {
	h, store := newStashTestHandlers(t)
	id1 := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "", "alpha\n")
	id2 := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "", "beta\n")
	if id1 == id2 {
		t.Errorf("different bodies must produce different IDs; both were %q", id1)
	}
}

// TestStashContent_DifferentSourceDifferentID verifies that the same body
// emitted from different sources (two files with identical content) gets
// distinct IDs so the agent can disambiguate.
func TestStashContent_DifferentSourceDifferentID(t *testing.T) {
	h, store := newStashTestHandlers(t)
	body := "shared content\n"
	id1 := h.stashContent(store, "/proj", "file-read", "/path/a.txt", "", body)
	id2 := h.stashContent(store, "/proj", "file-read", "/path/b.txt", "", body)
	if id1 == id2 {
		t.Errorf("different sources must produce different IDs; both were %q", id1)
	}
}

// TestStashContent_DedupExpires verifies entries past TTL no longer dedup.
// We poke the cache directly to backdate the entry rather than sleeping
// dedupTTL seconds — fast and deterministic.
func TestStashContent_DedupExpires(t *testing.T) {
	h, store := newStashTestHandlers(t)
	body := "expiring body\n"
	id1 := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "", body)
	key := stashDedupKey("/proj", "exec-stdout", "sandbox.exec", body)

	// Backdate the entry past TTL.
	h.dedupMu.Lock()
	e := h.dedupCache[key]
	e.expiresAt = time.Now().Add(-time.Second)
	h.dedupCache[key] = e
	h.dedupMu.Unlock()

	id2 := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "", body)
	if id2 == id1 {
		t.Errorf("expired entry must NOT dedup; got same ID %q", id1)
	}
}

// TestStashContent_DedupCapEvicts verifies the cache stays at or below
// dedupCap entries even under pathological insert pressure.
func TestStashContent_DedupCapEvicts(t *testing.T) {
	h, store := newStashTestHandlers(t)
	for i := 0; i < dedupCap*2; i++ {
		// Each call uses a unique source so all entries are distinct keys.
		h.stashContent(store, "/proj", "exec-stdout", "src", "", string(rune('a'+(i%26)))+"\n"+itoa(i))
	}
	h.dedupMu.Lock()
	size := len(h.dedupCache)
	h.dedupMu.Unlock()
	if size > dedupCap {
		t.Errorf("dedup cache must respect cap %d; got %d entries", dedupCap, size)
	}
}

// TestStashContent_EmptyBodyReturnsEmpty verifies the empty-body short
// circuit still applies — we don't pollute the dedup cache with the empty
// hash.
func TestStashContent_EmptyBodyReturnsEmpty(t *testing.T) {
	h, store := newStashTestHandlers(t)
	id := h.stashContent(store, "/proj", "exec-stdout", "sandbox.exec", "", "")
	if id != "" {
		t.Errorf("empty body must return empty ID; got %q", id)
	}
	h.dedupMu.Lock()
	size := len(h.dedupCache)
	h.dedupMu.Unlock()
	if size != 0 {
		t.Errorf("empty body must not populate cache; got %d entries", size)
	}
}

// TestStashContent_NoStoreReturnsEmpty verifies nil-store behavior is
// preserved — dedup must not fabricate a non-empty ID when there's nowhere
// to put the bytes.
func TestStashContent_NoStoreReturnsEmpty(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	id := h.stashContent(nil, "/proj", "exec-stdout", "sandbox.exec", "", "body\n")
	if id != "" {
		t.Errorf("no-store must return empty ID; got %q", id)
	}
}

// TestStashContent_CrossProjectKeysIsolated proves the dedup cache
// segregates entries by project_id. Without this, project A and
// project B served by one global daemon would collapse identical
// bytes to a single chunk-set ID — pointing into A's store. B's
// follow-up "give me content_id X" lookup would 404.
func TestStashContent_CrossProjectKeysIsolated(t *testing.T) {
	h, _ := newStashTestHandlers(t)
	storeA, errA := content.NewStore(content.StoreOptions{Path: filepath.Join(t.TempDir(), "a"), MaxSize: 1 << 20})
	storeB, errB := content.NewStore(content.StoreOptions{Path: filepath.Join(t.TempDir(), "b"), MaxSize: 1 << 20})
	if errA != nil || errB != nil {
		t.Fatalf("NewStore: %v %v", errA, errB)
	}
	body := "shared bytes across projects\n"
	idA := h.stashContent(storeA, "/proj/a", "fetch", "https://x", "", body)
	idB := h.stashContent(storeB, "/proj/b", "fetch", "https://x", "", body)
	if idA == "" || idB == "" {
		t.Fatalf("empty IDs: A=%q B=%q", idA, idB)
	}
	if idA == idB {
		t.Errorf("project A and B got same content_id %q — would 404 cross-project lookups", idA)
	}
	// And both stores contain their own copy.
	if _, ok := storeA.GetChunkSet(idA); !ok {
		t.Errorf("storeA missing idA")
	}
	if _, ok := storeB.GetChunkSet(idB); !ok {
		t.Errorf("storeB missing idB")
	}
}

// itoa is a tiny stdlib-free int-to-string helper for the cap test (avoids
// pulling strconv into a single-use spot).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

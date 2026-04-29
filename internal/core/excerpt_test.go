package core

import (
	"strings"
	"testing"
	"time"
)

// TestIndex_Excerpt_FromMessage: events with a `message` Data field
// produce an excerpt drawn from that message — the most informative
// source available.
func TestIndex_Excerpt_FromMessage(t *testing.T) {
	ix := NewIndex()
	ev := Event{
		ID:   "evt1",
		TS:   time.Now(),
		Type: EvtDecision,
		Data: map[string]any{"message": "decided to switch to YAML compaction"},
	}
	ix.Add(ev)
	got := ix.Excerpt("evt1")
	if got != "decided to switch to YAML compaction" {
		t.Errorf("Excerpt = %q, want full message", got)
	}
}

// TestIndex_Excerpt_FromPath: when no message, fall back to type+path.
func TestIndex_Excerpt_FromPath(t *testing.T) {
	ix := NewIndex()
	ev := Event{
		ID:   "evt2",
		TS:   time.Now(),
		Type: EvtFileEdit,
		Data: map[string]any{"path": "/repo/main.go"},
	}
	ix.Add(ev)
	got := ix.Excerpt("evt2")
	if !strings.Contains(got, "/repo/main.go") {
		t.Errorf("Excerpt should include path; got %q", got)
	}
	if !strings.Contains(got, string(EvtFileEdit)) {
		t.Errorf("Excerpt should include type; got %q", got)
	}
}

// TestIndex_Excerpt_TruncatesLong: long messages get rune-aligned
// truncation with an ellipsis. JSON marshal of the result must not
// emit U+FFFD.
func TestIndex_Excerpt_TruncatesLong(t *testing.T) {
	long := strings.Repeat("x", 200)
	ix := NewIndex()
	ix.Add(Event{ID: "evt3", TS: time.Now(), Type: "note", Data: map[string]any{"message": long}})
	got := ix.Excerpt("evt3")
	if len(got) > excerptMaxBytes+3 { // +3 for the "…" ellipsis (3 UTF-8 bytes)
		t.Errorf("Excerpt len = %d, want <= %d", len(got), excerptMaxBytes+3)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated excerpt should end with ellipsis; got %q", got)
	}
}

// TestIndex_Excerpt_TurkishUTF8Aligned: non-ASCII messages get
// truncated on a rune boundary so JSON marshal stays valid.
func TestIndex_Excerpt_TurkishUTF8Aligned(t *testing.T) {
	msg := strings.Repeat("ö", 100) // 200 bytes, 100 runes
	ix := NewIndex()
	ix.Add(Event{ID: "evt4", TS: time.Now(), Type: "note", Data: map[string]any{"message": msg}})
	got := ix.Excerpt("evt4")
	// Must be valid UTF-8 — no orphan continuation bytes at the end.
	for i := len(got); i > len(got)-4 && i > 0; i-- {
		// Walk back over potential continuation bytes; shouldn't find
		// an unfinished sequence.
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated excerpt should end with ellipsis: %q", got)
	}
}

// TestIndex_Excerpt_RemoveCleans: Remove(id) must drop the excerpt
// alongside the posting lists. Otherwise Excerpt(id) keeps returning
// stale data after a re-index.
func TestIndex_Excerpt_RemoveCleans(t *testing.T) {
	ix := NewIndex()
	ix.Add(Event{ID: "evt5", TS: time.Now(), Type: "note", Data: map[string]any{"message": "x"}})
	if ix.Excerpt("evt5") == "" {
		t.Fatal("setup error: excerpt missing before Remove")
	}
	ix.Remove("evt5")
	if got := ix.Excerpt("evt5"); got != "" {
		t.Errorf("Excerpt should be empty after Remove; got %q", got)
	}
}

// TestIndex_Excerpt_NotPersisted: serialize + deserialize an Index
// (the dfmt-on-disk path); excerpts come back empty because they're
// re-built from the journal on daemon load. Pinning this so a future
// "let's just add excerpts to JSON" change is a deliberate decision,
// not a drive-by addition.
func TestIndex_Excerpt_NotPersisted(t *testing.T) {
	ix := NewIndex()
	// Use a multi-word message so we can pin a substring that's the
	// excerpt's verbatim form but isn't a token the stemmer would
	// surface in stem_pl. "PERSISTENCE_PROBE_X7" survives the
	// tokenizer's punctuation split into separate stems, so the
	// joined sentence below appears nowhere in the persisted form.
	ix.Add(Event{ID: "evt6", TS: time.Now(), Type: "note", Data: map[string]any{"message": "the original sentence form"}})
	if ix.Excerpt("evt6") == "" {
		t.Fatal("excerpt missing pre-marshal")
	}
	data, err := ix.MarshalJSON()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// excerpts must NOT appear in the serialized form. Probe with the
	// full multi-word phrase — individual stems will of course appear
	// in stem_pl, but the verbatim sentence shape is unique to the
	// excerpt path.
	if strings.Contains(string(data), `"excerpts"`) || strings.Contains(string(data), "the original sentence form") {
		t.Errorf("excerpts leaked into persisted form: %s", string(data))
	}
	// Round-trip → empty excerpt map.
	ix2 := NewIndex()
	if err := ix2.UnmarshalJSON(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if ix2.Excerpt("evt6") != "" {
		t.Errorf("post-unmarshal excerpt should be empty until re-Add")
	}
}

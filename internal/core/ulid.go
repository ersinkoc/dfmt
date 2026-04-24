package core

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	lastTime   uint64
	lastRandom [10]byte
	muGen      sync.Mutex
	// ulidFallbackSeed mixes pid + monotonic counter for the
	// crypto/rand failure path. ULIDs minted through the fallback are still
	// unique-per-process (the counter guarantees it) but lose unpredictability.
	ulidFallbackCtr uint64
)

// ULID is a Universally Unique Lexicographically Sortable Identifier.
type ULID string

// NewULID generates a new ULID.
func NewULID(ts time.Time) ULID {
	muGen.Lock()
	defer muGen.Unlock()

	var b [16]byte

	// Timestamp (48 bits, milliseconds since Unix epoch)
	ms := uint64(ts.UnixMilli())

	// If same millisecond as last, increment the randomness (monotonic)
	if ms == lastTime {
		for i := 9; i >= 0; i-- {
			lastRandom[i]++
			if lastRandom[i] != 0 {
				break
			}
		}
	} else {
		lastTime = ms
		if _, err := rand.Read(lastRandom[:]); err != nil {
			// crypto/rand failure is extremely rare but non-fatal for our
			// purposes: fall back to a deterministic pid+counter+nanotime
			// mix so the daemon can still mint monotonic IDs instead of
			// crashing. Surface the error once so operators notice.
			fmt.Fprintf(os.Stderr, "warning: crypto/rand.Read failed for ULID: %v (using fallback seed)\n", err)
			ulidFallbackCtr++
			mix := uint64(os.Getpid())<<32 | ulidFallbackCtr
			binary.BigEndian.PutUint64(lastRandom[:8], mix^uint64(ts.UnixNano()))
			binary.BigEndian.PutUint16(lastRandom[8:], uint16(ulidFallbackCtr))
		}
	}

	// Encode timestamp (6 bytes)
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	// Encode randomness (10 bytes)
	copy(b[6:], lastRandom[:])

	return ULID(hex.EncodeToString(b[:]))
}

// Time extracts the timestamp from a ULID.
func (u ULID) Time() time.Time {
	b, _ := hex.DecodeString(string(u))
	if len(b) != 16 {
		return time.Time{}
	}
	ms := uint64(b[0])<<40 | uint64(b[1])<<32 | uint64(b[2])<<24 |
		uint64(b[3])<<16 | uint64(b[4])<<8 | uint64(b[5])
	return time.UnixMilli(int64(ms))
}

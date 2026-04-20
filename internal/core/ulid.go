package core

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

var (
	lastTime   uint64
	lastRandom [10]byte
	muGen      sync.Mutex
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
		rand.Read(lastRandom[:])
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

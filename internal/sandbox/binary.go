package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Binary content detection + refusal. When a fetched body or file read
// is non-UTF-8 (PNG/JPEG/PDF/gzipped tarball/etc.), the wire today
// ships the bytes as best-effort UTF-8 — mangled, surrogate-leaden
// noise that costs token budget and carries zero signal for an LLM.
// CompactBinary replaces such bodies with a one-line description:
//
//	(binary; type=image/png; 18327 bytes; sha256=ab12cd…)
//
// The agent gets the metadata it actually needs (size, type-hint,
// hash for re-fetch) and pays ~50-80 bytes instead of 18 KB. Callers
// that genuinely need the bytes opt out via Return:"raw" — but that
// path is broken for binary anyway (JSON-RPC can't carry it cleanly),
// so the refusal here is also a correctness improvement.

// binaryDetectSampleBytes caps the prefix we scan for binary
// signatures. 512 bytes is enough to catch any well-known magic
// number while staying cheap on small inputs.
const binaryDetectSampleBytes = 512

// binaryNullByteThreshold is the minimum number of NUL bytes in the
// detection sample that flags content as binary regardless of UTF-8
// validity. Pure-text encodings (UTF-8, UTF-16 LE/BE, Shift-JIS) do
// not contain unescaped NULs in their well-formed forms; finding
// even a handful is a strong signal.
const binaryNullByteThreshold = 1

// binaryMagicSignatures pins the file headers we recognise without a
// full UTF-8 scan. Each entry is the first few bytes of a common
// binary container; matching one bypasses the UTF-8 fallback (which
// some malformed-text inputs would also fail). The list is small
// because UTF-8 invalidation is the dominant detection path; magic
// numbers are the cheap fast-path.
var binaryMagicSignatures = []struct {
	prefix []byte
	mime   string
}{
	{prefix: []byte{0x89, 'P', 'N', 'G'}, mime: "image/png"},
	{prefix: []byte{0xFF, 0xD8, 0xFF}, mime: "image/jpeg"},
	{prefix: []byte("GIF8"), mime: "image/gif"},
	{prefix: []byte{0x52, 0x49, 0x46, 0x46}, mime: "image/webp-or-wav"}, // RIFF
	{prefix: []byte("%PDF-"), mime: "application/pdf"},
	{prefix: []byte{0x1F, 0x8B}, mime: "application/gzip"},
	{prefix: []byte{0x50, 0x4B, 0x03, 0x04}, mime: "application/zip"},
	{prefix: []byte{0x42, 0x5A, 0x68}, mime: "application/bzip2"},
	{prefix: []byte{0x7F, 'E', 'L', 'F'}, mime: "application/x-elf"},
	{prefix: []byte{0x4D, 0x5A}, mime: "application/x-msdownload"}, // PE/EXE
	{prefix: []byte("\xFD7zXZ\x00"), mime: "application/x-xz"},
	{prefix: []byte{0x00, 0x00, 0x01, 0xBA}, mime: "video/mpeg"},
	{prefix: []byte("OggS"), mime: "audio/ogg"},
	{prefix: []byte{0xFF, 0xFB}, mime: "audio/mpeg"},
}

// CompactBinary returns a one-line summary when s is detected as
// binary content; otherwise returns s unchanged. Detection is the
// union of (a) magic-number prefix match, and (b) UTF-8 invalidity
// or NUL bytes in the first sample. The summary embeds size and a
// short SHA-256 prefix so an agent can re-fetch with Return:"raw"
// for the byte-perfect copy if it really needs them.
//
// The function is pure and safe for concurrent use.
func CompactBinary(s string) string {
	if s == "" {
		return s
	}
	mime, isBinary := detectBinary(s)
	if !isBinary {
		return s
	}
	hash := sha256.Sum256([]byte(s))
	short := hex.EncodeToString(hash[:8])
	return fmt.Sprintf("(binary; type=%s; %d bytes; sha256=%s…)", mime, len(s), short)
}

// detectBinary returns (mimeHint, true) when s appears to be binary.
// The mimeHint is "application/octet-stream" when no magic number
// matched but the body still failed UTF-8 / NUL-byte checks.
func detectBinary(s string) (string, bool) {
	if mime, ok := matchBinaryMagic(s); ok {
		return mime, true
	}
	sample := s
	if len(sample) > binaryDetectSampleBytes {
		sample = sample[:binaryDetectSampleBytes]
	}
	// NUL bytes in a "text" body are almost always a binary signal —
	// well-formed UTF-8 / UTF-16 / Latin-N text never contains them.
	if strings.Count(sample, "\x00") >= binaryNullByteThreshold {
		return "application/octet-stream", true
	}
	if !utf8.ValidString(sample) {
		return "application/octet-stream", true
	}
	return "", false
}

// matchBinaryMagic checks the first few bytes against the known
// magic-number table. Returns the hinted MIME type and true on hit.
// The fast-path (one byte-prefix comparison per entry) keeps the
// detection cost dominated by the magic table size, not the input
// size.
func matchBinaryMagic(s string) (string, bool) {
	for _, sig := range binaryMagicSignatures {
		if len(s) < len(sig.prefix) {
			continue
		}
		if s[:len(sig.prefix)] == string(sig.prefix) {
			return sig.mime, true
		}
	}
	return "", false
}

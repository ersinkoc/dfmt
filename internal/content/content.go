package content

// Package content provides content chunk storage and summarization for
// sandboxed tool outputs. Content chunks are distinct from events: chunks
// store raw output bytes while events record what happened.
//
// Key concepts:
//   - Chunk: A piece of content from a single output (stdout, stderr, file read, HTTP body)
//   - ChunkSet: A collection of chunks sharing a common parent (e.g., all stdout from one exec)
//   - Store: A bounded, LRU-evicted store for chunks (default 64 MB)
//   - Summarizer: Produces human-readable summaries with warnings and top phrases
//
// Chunks are ephemeral by default (daemon lifetime); optional persistence
// writes them to .dfmt/content/ as gzipped JSONL.

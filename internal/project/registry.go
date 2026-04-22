package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Registry tracks all known DFMT projects.
type Registry struct {
	path string
	mu   sync.Mutex
}

// RegistryEntry represents a project entry in the registry.
type RegistryEntry struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	PID      int    `json:"pid,omitempty"`
	Socket   string `json:"socket"`
	LastSeen int64  `json:"last_seen"`
}

// NewRegistry creates or opens a project registry.
func NewRegistry(dataDir string) (*Registry, error) {
	path := filepath.Join(dataDir, "projects.jsonl")

	// Ensure directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create registry dir: %w", err)
	}

	return &Registry{path: path}, nil
}

// Add adds or updates a project entry.
func (r *Registry) Add(entry RegistryEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Read existing entries
	entries, err := r.readAll()
	if err != nil {
		return err
	}

	// Update or append
	found := false
	for i, e := range entries {
		if e.ID == entry.ID {
			entries[i] = entry
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, entry)
	}

	return r.writeAll(entries)
}

// Remove removes a project by ID.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := r.readAll()
	if err != nil {
		return err
	}

	var newEntries []RegistryEntry
	for _, e := range entries {
		if e.ID != id {
			newEntries = append(newEntries, e)
		}
	}

	return r.writeAll(newEntries)
}

// List returns all registered projects.
func (r *Registry) List() ([]RegistryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readAll()
}

// Get returns a project by ID.
func (r *Registry) Get(id string) (*RegistryEntry, error) {
	entries, err := r.List()
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.ID == id {
			return &e, nil
		}
	}
	return nil, nil
}

// readAll reads all entries from the registry file.
func (r *Registry) readAll() ([]RegistryEntry, error) {
	f, err := os.Open(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []RegistryEntry
	dec := json.NewDecoder(f)
	for dec.More() {
		var entry RegistryEntry
		if err := dec.Decode(&entry); err != nil {
			continue // Skip malformed lines
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// writeAll writes all entries to the registry file.
func (r *Registry) writeAll(entries []RegistryEntry) error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}

	return nil
}

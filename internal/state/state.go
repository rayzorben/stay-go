// Package state handles persistence of the managed-node registry to disk.
// The state file is a JSON document stored at ~/.local/share/stay-go/state.json
// and tracks which resources stay-go is responsible for, along with a hash of
// their last-applied config so that changes can be detected.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is the root of the persisted registry.
type State struct {
	Hostname  string               `json:"hostname"`
	UpdatedAt time.Time            `json:"updated_at"`
	Nodes     map[string]NodeEntry `json:"nodes"`
}

// NodeEntry records a single managed resource node.
type NodeEntry struct {
	Hash      string                 `json:"hash"`
	Level     string                 `json:"level,omitempty"`
	TrackedAt time.Time              `json:"tracked_at"`
	UpdatedAt time.Time              `json:"updated_at"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// DefaultPath returns the conventional state file location.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".stay-go/state.json"
	}
	return filepath.Join(home, ".local", "share", "stay-go", "state.json")
}

// Load reads state from disk. If the file does not exist an empty, valid State
// is returned without error — this is the correct first-run behaviour.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		hostname, _ := os.Hostname()
		return &State{
			Hostname: hostname,
			Nodes:    make(map[string]NodeEntry),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state %q: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing state %q: %w", path, err)
	}
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeEntry)
	}
	return &s, nil
}

// Save atomically writes state to disk. It writes to a temporary file first,
// then renames it to prevent corruption on crash. Parent directories are
// created as needed.
func Save(s *State, path string) error {
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("serialising state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temporary state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("committing state: %w", err)
	}
	return nil
}

// Get returns the NodeEntry for id and whether it was present.
func (s *State) Get(id string) (NodeEntry, bool) {
	e, ok := s.Nodes[id]
	return e, ok
}

// Set upserts a NodeEntry for id, storing the hash, level, and the full
// config data that was last applied. Data is used on subsequent runs to
// produce accurate change descriptions without querying the live system.
func (s *State) Set(id, hash, level string, data map[string]interface{}) {
	now := time.Now()
	entry, exists := s.Nodes[id]
	if !exists {
		entry = NodeEntry{TrackedAt: now}
	}
	entry.Hash = hash
	entry.UpdatedAt = now
	if level != "" {
		entry.Level = level
	}
	if data != nil {
		entry.Data = data
	}
	s.Nodes[id] = entry
}

// Delete removes the node with id from state. No-op if not present.
func (s *State) Delete(id string) {
	delete(s.Nodes, id)
}

// Has reports whether id is currently tracked.
func (s *State) Has(id string) bool {
	_, ok := s.Nodes[id]
	return ok
}

// Package json implements the "json" resource.
//
// File layout (Single Responsibility per file):
//
//	json.go      — Resource struct, constructor, Type()
//	knowledge.go — GatherKnowledge
//	plan.go      — BuildPlan
//	execute.go   — Execute
package json

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/executor"
)

// Resource manages JSON file value overrides: it applies desired values at
// specific JSON paths and restores the original values on removal.
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.JsonEntry // nodeID → config entry
}

// New creates a json Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{
		cfg:         cfg,
		exec:        exec,
		nodeConfigs: make(map[string]*config.JsonEntry),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "json" }

// nodeID returns the canonical node ID for a json file path.
func nodeID(filePath string) string { return "json/" + filePath }

// readFile reads and parses a JSON file. Returns nil map if the file does not exist.
func readFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parsing %q: %w", path, err)
	}
	return out, nil
}

// writeFile atomically writes a JSON document to path.
func writeFile(path string, doc map[string]interface{}) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling JSON for %q: %w", path, err)
	}
	tmp := path + ".stay-go.tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("committing %q: %w", path, err)
	}
	return nil
}

// getPath reads the value at a gron-style path (e.g. "json.foo[0].bar") from doc.
// The leading "json." segment is stripped; the remainder is the actual path.
// Returns (value, true) if the path exists, (nil, false) otherwise.
func getPath(doc map[string]interface{}, gronPath string) (interface{}, bool) {
	segments := parsePath(gronPath)
	var cur interface{} = doc
	for _, seg := range segments {
		switch t := cur.(type) {
		case map[string]interface{}:
			v, ok := t[seg.key]
			if !ok {
				return nil, false
			}
			cur = v
		case []interface{}:
			idx, err := strconv.Atoi(seg.key)
			if err != nil || idx < 0 || idx >= len(t) {
				return nil, false
			}
			cur = t[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// setPath sets the value at a gron-style path in doc, creating intermediate
// objects as needed. Returns an error if an intermediate node is a scalar.
func setPath(doc map[string]interface{}, gronPath string, value interface{}) error {
	segments := parsePath(gronPath)
	if len(segments) == 0 {
		return fmt.Errorf("empty path")
	}
	var cur interface{} = doc
	for i, seg := range segments[:len(segments)-1] {
		switch t := cur.(type) {
		case map[string]interface{}:
			next, ok := t[seg.key]
			if !ok {
				// Create intermediate map.
				next = map[string]interface{}{}
				t[seg.key] = next
			}
			cur = next
		case []interface{}:
			idx, err := strconv.Atoi(seg.key)
			if err != nil || idx < 0 || idx >= len(t) {
				return fmt.Errorf("index %d out of range at segment %d of %q", idx, i, gronPath)
			}
			cur = t[idx]
		default:
			return fmt.Errorf("cannot traverse into scalar at segment %d of %q", i, gronPath)
		}
	}
	last := segments[len(segments)-1]
	switch t := cur.(type) {
	case map[string]interface{}:
		t[last.key] = value
	case []interface{}:
		idx, err := strconv.Atoi(last.key)
		if err != nil || idx < 0 || idx >= len(t) {
			return fmt.Errorf("index %d out of range for last segment of %q", idx, gronPath)
		}
		t[idx] = value
	default:
		return fmt.Errorf("cannot set value: parent is a scalar at %q", gronPath)
	}
	return nil
}

// pathSegment is a single component of a parsed path.
type pathSegment struct{ key string }

// parsePath converts a gron-style path like "json.foo[0].bar" into segments.
// The leading "json." prefix is stripped if present.
func parsePath(gronPath string) []pathSegment {
	// Strip leading "json." prefix used in config YAML keys.
	p := strings.TrimPrefix(gronPath, "json.")
	// Split on "." and "[n]" patterns.
	var segs []pathSegment
	for p != "" {
		// Array index: [n]
		if p[0] == '[' {
			end := strings.Index(p, "]")
			if end < 0 {
				break
			}
			segs = append(segs, pathSegment{key: p[1:end]})
			p = strings.TrimPrefix(p[end+1:], ".")
			continue
		}
		// Object key: everything up to the next "." or "["
		end := strings.IndexAny(p, ".[")
		if end < 0 {
			segs = append(segs, pathSegment{key: p})
			break
		}
		segs = append(segs, pathSegment{key: p[:end]})
		if p[end] == '.' {
			p = p[end+1:]
		} else {
			p = p[end:]
		}
	}
	return segs
}

package config

import (
	"strconv"
)

// NormalizeExpandedForms turns shorthand config into the canonical shapes the
// rest of stay-go expects: package-embedded services become top-level service
// entries with an implicit depends on that package.
// Distrobox package services are intentionally left intact so that writeBoxConfig
// can expand them into the in-box guest config.
func NormalizeExpandedForms(cfg *Config) {
	expandPackageEmbeddedServices(cfg)
}

func expandPackageEmbeddedServices(cfg *Config) {
	var additions []ServiceEntry
	for _, p := range cfg.Packages {
		for _, svc := range p.Services {
			se := svc
			se.Depends = appendOneResourceDepAll(se.Depends, "packages", p.Name)
			if se.Level == "" {
				se.Level = p.Level
			}
			additions = append(additions, se)
		}
	}
	for i := range cfg.Packages {
		cfg.Packages[i].Services = nil
	}
	if len(additions) == 0 {
		return
	}
	cfg.Services = mergeServiceEntries(serviceList(cfg.Services), additions)
}

// serviceMergeKey distinguishes system vs user services with the same name.
func serviceMergeKey(s ServiceEntry) string {
	return s.Service + "\x00" + strconv.FormatBool(s.User)
}

func mergeServiceEntries(existing []ServiceEntry, fromPackages []ServiceEntry) []ServiceEntry {
	result := append([]ServiceEntry(nil), existing...)
	byKey := make(map[string]int, len(result))
	for i := range result {
		byKey[serviceMergeKey(result[i])] = i
	}
	for _, add := range fromPackages {
		k := serviceMergeKey(add)
		if idx, ok := byKey[k]; ok {
			result[idx].Depends = mergeDependsLists(result[idx].Depends, add.Depends)
			continue
		}
		byKey[k] = len(result)
		result = append(result, add)
	}
	return result
}

func mergeDependsLists(a, b []map[string][]string) []map[string][]string {
	out := append([]map[string][]string(nil), a...)
	for _, bm := range b {
		for resType, names := range bm {
			for _, n := range names {
				out = appendOneResourceDepAll(out, resType, n)
			}
		}
	}
	return out
}

// AppendResourceDep appends a single resource dep entry (e.g. "packages"/"foo")
// to a depends list, merging into an existing entry for that resource type when
// one already exists and deduplicating the name.
func AppendResourceDep(deps []map[string][]string, resType, name string) []map[string][]string {
	return appendOneResourceDepAll(deps, resType, name)
}

func appendOneResourceDepAll(deps []map[string][]string, resType, name string) []map[string][]string {
	for i := range deps {
		if names, ok := deps[i][resType]; ok {
			for _, existing := range names {
				if existing == name {
					return deps
				}
			}
			deps[i][resType] = append(names, name)
			return deps
		}
	}
	return append(deps, map[string][]string{resType: {name}})
}

// ServiceMergeKey returns the key used to merge and dedupe service entries
// (same service name can exist for system vs user scope).
func ServiceMergeKey(s ServiceEntry) string {
	return serviceMergeKey(s)
}

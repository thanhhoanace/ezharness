package evidence

// Shared glob matcher for v6.3 (G3/G16). ONE tested matcher is used by the loop
// forbidden-path brake, the lessons resolver, and (via the gate hook) risk-path
// gating, replacing the divergent python fnmatch at ezh-evidence-gate.sh:256
// where `*` crossed `/`.
//
// Safe subset (lint-enforced, identical semantics to .claude/rules globs):
//   - `prefix/**`         — prefix then any number of segments
//   - `**/segment/**`     — segment anywhere in the tree
//   - `*`                 — confined to ONE path segment (never crosses `/`)
//   - no `?`, no `{}` in v6.3
// `**` must be a whole path segment; `*` matches within a single segment only.

import (
	"errors"
	"strings"
)

// validateGlobPattern enforces the v6.3 safe subset. It returns an error for any
// pattern the resolver would treat ambiguously so `lesson lint` and contract
// validation reject it up front rather than matching unpredictably.
func validateGlobPattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return errors.New("empty pattern")
	}
	if strings.ContainsAny(pattern, "?{}") {
		return errors.New("`?`, `{`, and `}` are not allowed in the v6.3 safe subset")
	}
	for _, seg := range strings.Split(pattern, "/") {
		if strings.Contains(seg, "**") && seg != "**" {
			return errors.New("`**` must be its own path segment (e.g. `prefix/**`, not `pre**fix`)")
		}
	}
	return nil
}

// matchGlob reports whether path matches pattern under the safe subset. path is a
// repo-relative, slash-separated path. Matching is deterministic and replayable
// (no filesystem access).
func matchGlob(pattern string, path string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

// matchAnyGlob reports whether path matches any of the patterns.
func matchAnyGlob(patterns []string, path string) bool {
	for _, p := range patterns {
		if matchGlob(p, path) {
			return true
		}
	}
	return false
}

func matchSegments(pat []string, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	head := pat[0]
	if head == "**" {
		if len(pat) == 1 {
			return true // `**` (as tail) matches the remaining segments, incl. none
		}
		for i := 0; i <= len(path); i++ {
			if matchSegments(pat[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if !matchSegment(head, path[0]) {
		return false
	}
	return matchSegments(pat[1:], path[1:])
}

// matchSegment matches a single path segment against a pattern segment that may
// contain `*` wildcards (each `*` matches any run of characters, but never `/`
// — segments are already split on `/`).
func matchSegment(pattern string, segment string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == segment
	}
	parts := strings.Split(pattern, "*")
	// The first part must be a prefix.
	if !strings.HasPrefix(segment, parts[0]) {
		return false
	}
	segment = segment[len(parts[0]):]
	// The last part must be a suffix.
	last := parts[len(parts)-1]
	if !strings.HasSuffix(segment, last) {
		return false
	}
	segment = segment[:len(segment)-len(last)]
	// Middle parts must appear in order.
	for _, mid := range parts[1 : len(parts)-1] {
		if mid == "" {
			continue
		}
		idx := strings.Index(segment, mid)
		if idx < 0 {
			return false
		}
		segment = segment[idx+len(mid):]
	}
	return true
}

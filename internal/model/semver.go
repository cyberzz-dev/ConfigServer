// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

import (
	"fmt"
	"strconv"
	"strings"
)

// MatchVersionConstraint reports whether agentVersion satisfies constraint.
//
// constraint supports two levels of logic:
//   - OR groups are separated by "||" (any one group matching is sufficient)
//   - Within each OR group, clauses are comma-separated (AND logic, all must match)
//
// Each clause has the form "<op> <version>" where op is one of: >=, >, <=, <, =.
// An empty constraint matches all versions.
//
// Version strings are compared numerically segment by segment (e.g. "1.10.0"
// is greater than "1.9.0"). Non-numeric trailing labels (e.g. "-beta") are
// stripped before comparison.
//
// Examples:
//
//	MatchVersionConstraint("", "1.2.3")                              // true
//	MatchVersionConstraint(">= 1.2.0", "1.2.3")                     // true
//	MatchVersionConstraint(">= 1.2.0, < 2.0.0", "2.0.0")           // false
//	MatchVersionConstraint(">= 1.0.0, < 2.0.0 || >= 3.0.0", "3.1.0") // true
func MatchVersionConstraint(constraint, agentVersion string) bool {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return true
	}
	// Split into OR groups; any group matching makes the whole constraint true.
	orGroups := strings.Split(constraint, "||")
	for _, group := range orGroups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if matchAndGroup(group, agentVersion) {
			return true
		}
	}
	return false
}

// matchAndGroup checks whether agentVersion satisfies all comma-separated clauses in group.
func matchAndGroup(group, agentVersion string) bool {
	clauses := strings.Split(group, ",")
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		if !matchClause(clause, agentVersion) {
			return false
		}
	}
	return true
}

// ValidateVersionConstraint returns an error if constraint is syntactically invalid.
func ValidateVersionConstraint(constraint string) error {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return nil
	}
	orGroups := strings.Split(constraint, "||")
	for _, group := range orGroups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		clauses := strings.Split(group, ",")
		for _, clause := range clauses {
			clause = strings.TrimSpace(clause)
			if clause == "" {
				continue
			}
			_, _, err := parseClause(clause)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// matchClause evaluates a single clause like ">= 1.2.0" against agentVersion.
func matchClause(clause, agentVersion string) bool {
	op, ver, err := parseClause(clause)
	if err != nil {
		return false
	}
	cmp := compareVersions(agentVersion, ver)
	switch op {
	case ">=":
		return cmp >= 0
	case ">":
		return cmp > 0
	case "<=":
		return cmp <= 0
	case "<":
		return cmp < 0
	case "=", "==":
		return cmp == 0
	}
	return false
}

// parseClause parses a single constraint clause like ">= 1.2.0".
func parseClause(clause string) (op, version string, err error) {
	for _, o := range []string{">=", "<=", ">", "<", "=="} {
		if strings.HasPrefix(clause, o) {
			return o, strings.TrimSpace(clause[len(o):]), nil
		}
	}
	// bare "= x.y.z"
	if strings.HasPrefix(clause, "=") {
		return "=", strings.TrimSpace(clause[1:]), nil
	}
	return "", "", fmt.Errorf("invalid version constraint clause %q: expected operator (>=, >, <=, <, =)", clause)
}

// compareVersions compares two dotted-numeric version strings.
// Returns -1, 0, or +1.
// Non-numeric suffixes (like "-beta") are stripped.
func compareVersions(a, b string) int {
	segsA := versionSegments(a)
	segsB := versionSegments(b)
	maxLen := len(segsA)
	if len(segsB) > maxLen {
		maxLen = len(segsB)
	}
	for i := 0; i < maxLen; i++ {
		na, nb := 0, 0
		if i < len(segsA) {
			na = segsA[i]
		}
		if i < len(segsB) {
			nb = segsB[i]
		}
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
	}
	return 0
}

// versionSegments parses a version string into numeric segments.
// Non-numeric trailing text in each segment is ignored (e.g. "1-beta" → 1).
func versionSegments(v string) []int {
	// Strip leading "v" prefix (e.g. "v1.2.3" → "1.2.3")
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	segs := make([]int, 0, len(parts))
	for _, p := range parts {
		// Strip non-numeric suffix (e.g. "3-beta" → "3")
		for i, c := range p {
			if c == '-' || c == '+' {
				p = p[:i]
				break
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		segs = append(segs, n)
	}
	return segs
}

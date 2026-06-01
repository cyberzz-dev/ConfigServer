// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

import (
	"encoding/json"
	"hash/fnv"
)

// CanaryTag is a single key-value tag used by a canary's TagSelector.
type CanaryTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CanaryTagSelector is the parsed form of CanaryRelease.TagSelectorJSON.
type CanaryTagSelector struct {
	Tags []CanaryTag `json:"tags"`
}

// stableKey returns the per-host stable identity used for canary bucketing.
//
// Priority order:
//  1. Hostid  — OTel host.id, machine-level, survives process restarts (preferred)
//  2. Hostname — falls back to hostname when host.id is not populated
//
// Returns (key, true) when a stable key is available, ("", false) otherwise.
// Callers that receive ok=false must skip canary delivery and serve the stable version.
func stableKey(m AgentMatchContext) (string, bool) {
	if m.Hostid != "" {
		return m.Hostid, true
	}
	if m.Hostname != "" {
		return m.Hostname, true
	}
	return "", false
}

// CanaryBucket returns a stable bucket in [0, 100) for the (host, configName) pair.
//
// The hash is FNV-32a over: stableKey + NUL + configName. The NUL byte prevents
// collisions between concatenations like ("ab","cd") vs ("a","bcd").
//
// Returns ok=false when no stable host identity is available; callers must fall
// back to the stable (non-canary) config version.
func CanaryBucket(m AgentMatchContext, configName string) (bucket int, ok bool) {
	key, ok := stableKey(m)
	if !ok {
		return 0, false
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	h.Write([]byte{0}) // separator — prevents (key+name) prefix collisions
	h.Write([]byte(configName))
	return int(h.Sum32() % 100), true
}

// CanaryMatchesIP reports whether the agent's IP satisfies the canary's IP
// selector. An empty selector imposes no restriction and returns true.
func CanaryMatchesIP(ipSelectorJSON, ip string) (bool, error) {
	if ipSelectorJSON == "" {
		return true, nil
	}
	if ip == "" {
		return false, nil
	}
	return IPSelectorMatches(ipSelectorJSON, ip)
}

// CanaryMatchesTags reports whether the agent carries at least one of the tags
// listed in the canary's tag selector (ANY-match). An empty selector imposes no
// restriction and returns true.
func CanaryMatchesTags(tagSelectorJSON string, tags []AgentGroupTag) (bool, error) {
	if tagSelectorJSON == "" {
		return true, nil
	}
	var sel CanaryTagSelector
	if err := json.Unmarshal([]byte(tagSelectorJSON), &sel); err != nil {
		return false, err
	}
	if len(sel.Tags) == 0 {
		return true, nil
	}
	for _, want := range sel.Tags {
		for _, have := range tags {
			if have.TagName == want.Name && have.TagValue == want.Value {
				return true, nil
			}
		}
	}
	return false, nil
}

// CanaryEligible reports whether an agent is eligible to receive the canary
// version of configName. It combines, in order: version constraint, IP selector,
// tag selector (all must pass — AND), and finally the percentage bucket.
// Returns false when no stable host identity exists (cannot bucket safely).
func CanaryEligible(cr *CanaryRelease, m AgentMatchContext, configName string) (bool, error) {
	if cr.VersionConstraint != "" && !MatchVersionConstraint(cr.VersionConstraint, m.Version) {
		return false, nil
	}
	ipOK, err := CanaryMatchesIP(cr.IPSelectorJSON, m.IP)
	if err != nil {
		return false, err
	}
	if !ipOK {
		return false, nil
	}
	tagOK, err := CanaryMatchesTags(cr.TagSelectorJSON, m.Tags)
	if err != nil {
		return false, err
	}
	if !tagOK {
		return false, nil
	}
	bucket, ok := CanaryBucket(m, configName)
	if !ok {
		return false, nil
	}
	return bucket < cr.RolloutPercent, nil
}

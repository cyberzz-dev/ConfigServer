// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package model

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

func ParseIPSelectorJSON(raw string) (AgentGroupIPSelector, error) {
	if strings.TrimSpace(raw) == "" {
		return AgentGroupIPSelector{IPs: []string{}}, nil
	}
	var selector AgentGroupIPSelector
	if err := json.Unmarshal([]byte(raw), &selector); err != nil {
		return AgentGroupIPSelector{}, fmt.Errorf("invalid ip selector json: %w", err)
	}
	return NormalizeIPSelector(selector)
}

func NormalizeIPSelector(selector AgentGroupIPSelector) (AgentGroupIPSelector, error) {
	items := make([]string, 0, len(selector.IPs))
	seen := make(map[string]struct{}, len(selector.IPs))
	for _, item := range selector.IPs {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, err := parseIPSelectorItem(item); err != nil {
			return AgentGroupIPSelector{}, err
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		items = append(items, item)
	}
	return AgentGroupIPSelector{IPs: items}, nil
}

func MarshalIPSelector(selector AgentGroupIPSelector) (string, error) {
	normalized, err := NormalizeIPSelector(selector)
	if err != nil {
		return "", err
	}
	if len(normalized.IPs) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func IPSelectorMatches(raw string, ip string) (bool, error) {
	selector, err := ParseIPSelectorJSON(raw)
	if err != nil {
		return false, err
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false, nil
	}
	for _, item := range selector.IPs {
		rule, err := parseIPSelectorItem(item)
		if err != nil {
			return false, err
		}
		if rule.matches(addr) {
			return true, nil
		}
	}
	return false, nil
}

type ipSelectorItem struct {
	addr   netip.Addr
	start  netip.Addr
	end    netip.Addr
	prefix netip.Prefix
	kind   string
}

func parseIPSelectorItem(item string) (ipSelectorItem, error) {
	if strings.Contains(item, "/") {
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return ipSelectorItem{}, fmt.Errorf("invalid ip cidr %q: %w", item, err)
		}
		return ipSelectorItem{prefix: prefix, kind: "prefix"}, nil
	}
	if strings.Contains(item, "-") {
		parts := strings.Split(item, "-")
		if len(parts) != 2 {
			return ipSelectorItem{}, fmt.Errorf("invalid ip range %q", item)
		}
		start, err := netip.ParseAddr(strings.TrimSpace(parts[0]))
		if err != nil {
			return ipSelectorItem{}, fmt.Errorf("invalid ip range start %q: %w", item, err)
		}
		endText := strings.TrimSpace(parts[1])
		end, err := parseRangeEnd(start, endText)
		if err != nil {
			return ipSelectorItem{}, fmt.Errorf("invalid ip range end %q: %w", item, err)
		}
		if start.Compare(end) > 0 {
			return ipSelectorItem{}, fmt.Errorf("invalid ip range %q: start is greater than end", item)
		}
		return ipSelectorItem{start: start, end: end, kind: "range"}, nil
	}
	addr, err := netip.ParseAddr(item)
	if err != nil {
		return ipSelectorItem{}, fmt.Errorf("invalid ip %q: %w", item, err)
	}
	return ipSelectorItem{addr: addr, kind: "addr"}, nil
}

func parseRangeEnd(start netip.Addr, endText string) (netip.Addr, error) {
	if strings.Contains(endText, ".") || strings.Contains(endText, ":") {
		return netip.ParseAddr(endText)
	}
	if !start.Is4() {
		return netip.Addr{}, fmt.Errorf("short range end only supports IPv4")
	}
	suffix, err := strconv.Atoi(endText)
	if err != nil {
		return netip.Addr{}, err
	}
	if suffix < 0 || suffix > 255 {
		return netip.Addr{}, fmt.Errorf("last octet out of range")
	}
	octets := start.As4()
	octets[3] = byte(suffix)
	return netip.AddrFrom4(octets), nil
}

func (item ipSelectorItem) matches(addr netip.Addr) bool {
	switch item.kind {
	case "addr":
		return addr == item.addr
	case "range":
		return addr.Compare(item.start) >= 0 && addr.Compare(item.end) <= 0
	case "prefix":
		return item.prefix.Contains(addr)
	default:
		return false
	}
}

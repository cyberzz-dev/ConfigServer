// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package model

import "testing"

func TestIPSelectorMatches(t *testing.T) {
	selectorJSON, err := MarshalIPSelector(AgentGroupIPSelector{IPs: []string{
		"192.168.1.2",
		"192.168.1.200-230",
		"10.0.0.0/24",
		"172.16.1.10-172.16.1.12",
	}})
	if err != nil {
		t.Fatalf("MarshalIPSelector() error = %v", err)
	}

	cases := []struct {
		name string
		ip   string
		want bool
	}{
		{name: "single", ip: "192.168.1.2", want: true},
		{name: "short range", ip: "192.168.1.220", want: true},
		{name: "cidr", ip: "10.0.0.42", want: true},
		{name: "full range", ip: "172.16.1.11", want: true},
		{name: "outside range", ip: "192.168.1.231", want: false},
		{name: "invalid agent ip", ip: "not-an-ip", want: false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IPSelectorMatches(selectorJSON, tt.ip)
			if err != nil {
				t.Fatalf("IPSelectorMatches() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("IPSelectorMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeIPSelectorRejectsInvalidRules(t *testing.T) {
	cases := []AgentGroupIPSelector{
		{IPs: []string{"192.168.1.230-200"}},
		{IPs: []string{"192.168.1.300"}},
		{IPs: []string{"2001:db8::1-3"}},
	}

	for _, selector := range cases {
		if _, err := NormalizeIPSelector(selector); err == nil {
			t.Fatalf("NormalizeIPSelector(%v) expected error", selector)
		}
	}
}

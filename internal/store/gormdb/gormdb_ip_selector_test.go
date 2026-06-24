// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package gormdb

import (
	"context"
	"testing"

	"github.com/alibaba/ilogtail/config_server/internal/model"
)

func TestGetConfigsForAgentMatchesIPSelector(t *testing.T) {
	ctx := context.Background()
	store, err := New("sqlite", t.TempDir()+"/config.db", PoolConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	selectorJSON, err := model.MarshalIPSelector(model.AgentGroupIPSelector{IPs: []string{"192.168.1.200-230"}})
	if err != nil {
		t.Fatalf("MarshalIPSelector() error = %v", err)
	}
	if err := store.CreateGroup(ctx, &model.AgentGroup{Name: "app_group1", IPSelectorJSON: selectorJSON}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if err := store.CreatePipelineConfig(ctx, &model.PipelineConfig{Name: "app_pipeline", Version: 1, Detail: []byte("enable: true")}); err != nil {
		t.Fatalf("CreatePipelineConfig() error = %v", err)
	}
	if err := store.AddGroupConfig(ctx, &model.GroupConfigMapping{GroupName: "app_group1", ConfigName: "app_pipeline", ConfigType: model.ConfigTypePipeline}); err != nil {
		t.Fatalf("AddGroupConfig() error = %v", err)
	}

	pipelines, _, _, err := store.GetConfigsForAgent(ctx, model.AgentMatchContext{IP: "192.168.1.220"})
	if err != nil {
		t.Fatalf("GetConfigsForAgent() error = %v", err)
	}
	if len(pipelines) != 1 || pipelines[0].Name != "app_pipeline" {
		t.Fatalf("GetConfigsForAgent() pipelines = %#v, want app_pipeline", pipelines)
	}

	pipelines, _, _, err = store.GetConfigsForAgent(ctx, model.AgentMatchContext{IP: "192.168.1.199"})
	if err != nil {
		t.Fatalf("GetConfigsForAgent() error = %v", err)
	}
	if len(pipelines) != 0 {
		t.Fatalf("GetConfigsForAgent() pipelines = %#v, want none", pipelines)
	}
}

func TestGetConfigsForAgentMatchesVersionConstraintOnlyGroup(t *testing.T) {
	ctx := context.Background()
	store, err := New("sqlite", t.TempDir()+"/config.db", PoolConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := store.CreateGroup(ctx, &model.AgentGroup{Name: "version_group", VersionConstraint: ">= 3.3.6"}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if err := store.CreatePipelineConfig(ctx, &model.PipelineConfig{Name: "version_pipeline", Version: 1, Detail: []byte("enable: true")}); err != nil {
		t.Fatalf("CreatePipelineConfig() error = %v", err)
	}
	if err := store.AddGroupConfig(ctx, &model.GroupConfigMapping{GroupName: "version_group", ConfigName: "version_pipeline", ConfigType: model.ConfigTypePipeline}); err != nil {
		t.Fatalf("AddGroupConfig() error = %v", err)
	}

	pipelines, _, _, err := store.GetConfigsForAgent(ctx, model.AgentMatchContext{Version: "3.3.6"})
	if err != nil {
		t.Fatalf("GetConfigsForAgent() error = %v", err)
	}
	if len(pipelines) != 1 || pipelines[0].Name != "version_pipeline" {
		t.Fatalf("GetConfigsForAgent() pipelines = %#v, want version_pipeline", pipelines)
	}

	pipelines, _, _, err = store.GetConfigsForAgent(ctx, model.AgentMatchContext{Version: "3.3.5"})
	if err != nil {
		t.Fatalf("GetConfigsForAgent() error = %v", err)
	}
	if len(pipelines) != 0 {
		t.Fatalf("GetConfigsForAgent() pipelines = %#v, want none", pipelines)
	}
}

func TestGetConfigsForAgentMatchesExactVersionConstraintOnlyGroup(t *testing.T) {
	ctx := context.Background()
	store, err := New("sqlite", t.TempDir()+"/config.db", PoolConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := store.CreateGroup(ctx, &model.AgentGroup{Name: "exact_version_group", VersionConstraint: "== 3.3.6"}); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if err := store.CreatePipelineConfig(ctx, &model.PipelineConfig{Name: "exact_version_pipeline", Version: 1, Detail: []byte("enable: true")}); err != nil {
		t.Fatalf("CreatePipelineConfig() error = %v", err)
	}
	if err := store.AddGroupConfig(ctx, &model.GroupConfigMapping{GroupName: "exact_version_group", ConfigName: "exact_version_pipeline", ConfigType: model.ConfigTypePipeline}); err != nil {
		t.Fatalf("AddGroupConfig() error = %v", err)
	}

	pipelines, _, _, err := store.GetConfigsForAgent(ctx, model.AgentMatchContext{Version: "3.3.6"})
	if err != nil {
		t.Fatalf("GetConfigsForAgent() error = %v", err)
	}
	if len(pipelines) != 1 || pipelines[0].Name != "exact_version_pipeline" {
		t.Fatalf("GetConfigsForAgent() pipelines = %#v, want exact_version_pipeline", pipelines)
	}

	pipelines, _, _, err = store.GetConfigsForAgent(ctx, model.AgentMatchContext{Version: "3.3.7"})
	if err != nil {
		t.Fatalf("GetConfigsForAgent() error = %v", err)
	}
	if len(pipelines) != 0 {
		t.Fatalf("GetConfigsForAgent() pipelines = %#v, want none", pipelines)
	}
}

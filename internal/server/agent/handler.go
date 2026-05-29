// Copyright 2024 iLogtail Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

// Package agent implements the three agent-facing gRPC-over-HTTP endpoints.
package agent

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
	"github.com/alibaba/ilogtail/config_server/internal/model"
	protov2 "github.com/alibaba/ilogtail/config_server/proto/v2"
)

const (
	protoContentType = "application/x-protobuf"

	// maxInlineDetailBytes is the threshold (per config type) above which the
	// server strips inline detail bytes from the Heartbeat response and sets
	// the corresponding FetchDetail flag, instructing the agent to retrieve
	// full content via the /Agent/FetchConfig API instead.
	maxInlineDetailBytes = 256 * 1024 // 256 KiB
)

// AgentHandler implements the three agent-facing gRPC-over-HTTP endpoints.
type AgentHandler struct {
	mgr *cache.Manager
}

// NewAgentHandler creates an AgentHandler backed by the given cache manager.
func NewAgentHandler(mgr *cache.Manager) *AgentHandler {
	return &AgentHandler{mgr: mgr}
}

// RegisterAgentRoutes wires agent endpoints into mux.
func RegisterAgentRoutes(mux *http.ServeMux, h *AgentHandler) {
	mux.HandleFunc("POST /Agent/Heartbeat", h.Heartbeat)
	mux.HandleFunc("POST /Agent/FetchConfig", h.FetchConfig)
	mux.HandleFunc("POST /Agent/ReportStatus", h.ReportStatus)
}

// ── /Agent/Heartbeat ──────────────────────────────────────────────────────────

func (h *AgentHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	req := &protov2.HeartbeatRequest{}
	if err := proto.Unmarshal(body, req); err != nil {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	instanceID := string(req.InstanceId)

	// Detect server restart (stored SequenceNum=0 but agent's counter > 1).
	// In that case ask the agent to resend its full state.
	existingAgent, _ := h.mgr.GetAgent(ctx, instanceID)
	needFullState := existingAgent != nil &&
		existingAgent.SequenceNum == 0 &&
		req.SequenceNum > 1

	// Persist agent info.
	agent := buildAgent(req)
	if err := h.mgr.UpsertAgent(ctx, agent); err != nil {
		log.Printf("ERROR: upsert agent %s: %v", instanceID, err)
	}

	// Persist agent config statuses from the heartbeat.
	h.saveConfigStatuses(r, req, instanceID)

	// Resolve configs for this agent based on its tags.
	agentTags := protoTagsToModel(req.Tags)
	pipelines, instances, onetimes, err := h.mgr.GetConfigsForAgent(ctx, agentTags)
	if err != nil {
		log.Printf("ERROR: GetConfigsForAgent %s: %v", instanceID, err)
		writeProtoResponse(w, buildErrHBResp(req.RequestId, 1, "internal error"))
		return
	}

	// Diff: only include configs where the agent's version differs from server's.
	agentPipeline := indexConfigInfos(req.ContinuousPipelineConfigs)
	agentInstance := indexConfigInfos(req.InstanceConfigs)
	agentOnetime := indexConfigInfos(req.OnetimePipelineConfigs)
	fullState := req.Flags&uint64(protov2.RequestFlags_FullState) != 0

	var respFlags uint64
	if needFullState {
		respFlags |= uint64(protov2.ResponseFlags_ReportFullState)
	}

	resp := &protov2.HeartbeatResponse{
		RequestId:      req.RequestId,
		CommonResponse: &protov2.CommonResponse{Status: 0},
		Capabilities: uint64(protov2.ServerCapabilities_RembersAttribute) |
			uint64(protov2.ServerCapabilities_RembersContinuousPipelineConfigStatus) |
			uint64(protov2.ServerCapabilities_RembersInstanceConfigStatus) |
			uint64(protov2.ServerCapabilities_RembersOnetimePipelineConfigStatus),
		Flags: respFlags,
	}

	for _, pc := range pipelines {
		if len(pc.Detail) == 0 {
			continue // skip configs with empty content — not yet configured
		}
		clientVer, exists := agentPipeline[pc.Name]
		if !fullState && exists && clientVer == pc.Version {
			continue // agent already has this version
		}
		resp.ContinuousPipelineConfigUpdates = append(resp.ContinuousPipelineConfigUpdates, &protov2.ConfigDetail{
			Name:    pc.Name,
			Version: pc.Version,
			Detail:  pc.Detail,
		})
	}

	// Signal deletion for pipeline configs the agent has but the server no longer assigns.
	serverPipelineNames := make(map[string]struct{}, len(pipelines))
	for _, pc := range pipelines {
		if len(pc.Detail) > 0 {
			serverPipelineNames[pc.Name] = struct{}{}
		}
	}
	for name := range agentPipeline {
		if _, ok := serverPipelineNames[name]; !ok {
			resp.ContinuousPipelineConfigUpdates = append(resp.ContinuousPipelineConfigUpdates, &protov2.ConfigDetail{
				Name:    name,
				Version: -1,
			})
		}
	}

	for _, ic := range instances {
		if len(ic.Detail) == 0 {
			continue // skip configs with empty content
		}
		clientVer, exists := agentInstance[ic.Name]
		if !fullState && exists && clientVer == ic.Version {
			continue
		}
		resp.InstanceConfigUpdates = append(resp.InstanceConfigUpdates, &protov2.ConfigDetail{
			Name:    ic.Name,
			Version: ic.Version,
			Detail:  ic.Detail,
		})
	}

	// Signal deletion for instance configs the agent has but the server no longer assigns.
	serverInstanceNames := make(map[string]struct{}, len(instances))
	for _, ic := range instances {
		if len(ic.Detail) > 0 {
			serverInstanceNames[ic.Name] = struct{}{}
		}
	}
	for name := range agentInstance {
		if _, ok := serverInstanceNames[name]; !ok {
			resp.InstanceConfigUpdates = append(resp.InstanceConfigUpdates, &protov2.ConfigDetail{
				Name:    name,
				Version: -1,
			})
		}
	}

	now := time.Now().Unix()
	for _, oc := range onetimes {
		// Skip expired commands.
		if oc.ExpireTime > 0 && oc.ExpireTime < now {
			continue
		}
		// Deliver only if the agent hasn't reported it yet (version=0 means unknown).
		if _, seen := agentOnetime[oc.Name]; seen && !fullState {
			continue
		}
		resp.OnetimePipelineConfigUpdates = append(resp.OnetimePipelineConfigUpdates, &protov2.CommandDetail{
			Name:       oc.Name,
			Detail:     oc.Detail,
			ExpireTime: oc.ExpireTime,
		})
	}

	// If the total inline detail payload for continuous pipeline configs exceeds
	// the threshold, strip the detail bytes and set FetchContinuousPipelineConfigDetail
	// so the agent knows to call /Agent/FetchConfig for the actual content.
	var totalPipelineBytes int
	for _, cd := range resp.ContinuousPipelineConfigUpdates {
		totalPipelineBytes += len(cd.Detail)
	}
	if totalPipelineBytes > maxInlineDetailBytes {
		resp.Flags |= uint64(protov2.ResponseFlags_FetchContinuousPipelineConfigDetail)
		for _, cd := range resp.ContinuousPipelineConfigUpdates {
			cd.Detail = nil
		}
	}

	// Same for instance configs.
	var totalInstanceBytes int
	for _, cd := range resp.InstanceConfigUpdates {
		totalInstanceBytes += len(cd.Detail)
	}
	if totalInstanceBytes > maxInlineDetailBytes {
		resp.Flags |= uint64(protov2.ResponseFlags_FetchInstanceConfigDetail)
		for _, cd := range resp.InstanceConfigUpdates {
			cd.Detail = nil
		}
	}

	writeProtoResponse(w, resp)
}

// ── /Agent/FetchConfig ────────────────────────────────────────────────────────

func (h *AgentHandler) FetchConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	req := &protov2.FetchConfigRequest{}
	if err := proto.Unmarshal(body, req); err != nil {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	resp := &protov2.FetchConfigResponse{
		RequestId:      req.RequestId,
		CommonResponse: &protov2.CommonResponse{Status: 0},
	}

	for _, ci := range req.ContinuousPipelineConfigs {
		pc, err := h.mgr.GetPipelineConfig(ctx, ci.Name)
		if err != nil {
			if cache.IsNotFound(err) {
				// Config was deleted from server — instruct agent to remove it.
				resp.ContinuousPipelineConfigUpdates = append(resp.ContinuousPipelineConfigUpdates, &protov2.ConfigDetail{
					Name:    ci.Name,
					Version: -1,
				})
			}
			continue
		}
		if len(pc.Detail) == 0 {
			continue // skip configs with empty content
		}
		if pc.Version != ci.Version {
			resp.ContinuousPipelineConfigUpdates = append(resp.ContinuousPipelineConfigUpdates, &protov2.ConfigDetail{
				Name:    pc.Name,
				Version: pc.Version,
				Detail:  pc.Detail,
			})
		}
	}

	for _, ci := range req.InstanceConfigs {
		ic, err := h.mgr.GetInstanceConfig(ctx, ci.Name)
		if err != nil {
			if cache.IsNotFound(err) {
				// Config was deleted from server — instruct agent to remove it.
				resp.InstanceConfigUpdates = append(resp.InstanceConfigUpdates, &protov2.ConfigDetail{
					Name:    ci.Name,
					Version: -1,
				})
			}
			continue
		}
		if len(ic.Detail) == 0 {
			continue // skip configs with empty content
		}
		if ic.Version != ci.Version {
			resp.InstanceConfigUpdates = append(resp.InstanceConfigUpdates, &protov2.ConfigDetail{
				Name:    ic.Name,
				Version: ic.Version,
				Detail:  ic.Detail,
			})
		}
	}

	now := time.Now().Unix()
	for _, ci := range req.OnetimePipelineConfigs {
		oc, err := h.mgr.GetOnetimeCommand(ctx, ci.Name)
		if err != nil {
			continue
		}
		if oc.ExpireTime > 0 && oc.ExpireTime < now {
			continue
		}
		resp.OnetimePipelineConfigUpdates = append(resp.OnetimePipelineConfigUpdates, &protov2.CommandDetail{
			Name:       oc.Name,
			Detail:     oc.Detail,
			ExpireTime: oc.ExpireTime,
		})
	}

	writeProtoResponse(w, resp)
}

// ── /Agent/ReportStatus ───────────────────────────────────────────────────────

func (h *AgentHandler) ReportStatus(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	req := &protov2.ReportStatusRequest{}
	if err := proto.Unmarshal(body, req); err != nil {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}

	instanceID := string(req.InstanceId)
	h.saveStatusList(r, instanceID, req.ContinuousPipelineConfigs, model.ConfigTypePipeline)
	h.saveStatusList(r, instanceID, req.InstanceConfigs, model.ConfigTypeInstance)
	h.saveStatusList(r, instanceID, req.OnetimePipelineConfigs, model.ConfigTypeOnetime)

	writeProtoResponse(w, &protov2.ReportStatusResponse{
		RequestId:      req.RequestId,
		CommonResponse: &protov2.CommonResponse{Status: 0},
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (h *AgentHandler) saveConfigStatuses(r *http.Request, req *protov2.HeartbeatRequest, instanceID string) {
	ctx := r.Context()
	save := func(cfgs []*protov2.ConfigInfo, configType string) {
		for _, ci := range cfgs {
			if err := h.mgr.UpsertAgentConfigStatus(ctx, &model.AgentConfigStatus{
				InstanceID: instanceID,
				ConfigName: ci.Name,
				ConfigType: configType,
				Status:     int32(ci.Status),
				Message:    ci.Message,
			}); err != nil {
				log.Printf("WARN: upsert config status %s/%s: %v", instanceID, ci.Name, err)
			}
		}
	}
	save(req.ContinuousPipelineConfigs, model.ConfigTypePipeline)
	save(req.InstanceConfigs, model.ConfigTypeInstance)
	save(req.OnetimePipelineConfigs, model.ConfigTypeOnetime)
}

func (h *AgentHandler) saveStatusList(r *http.Request, instanceID string, cfgs []*protov2.ConfigInfo, configType string) {
	ctx := r.Context()
	for _, ci := range cfgs {
		if err := h.mgr.UpsertAgentConfigStatus(ctx, &model.AgentConfigStatus{
			InstanceID: instanceID,
			ConfigName: ci.Name,
			ConfigType: configType,
			Status:     int32(ci.Status),
			Message:    ci.Message,
		}); err != nil {
			log.Printf("WARN: upsert config status %s/%s: %v", instanceID, ci.Name, err)
		}
	}
}

func buildAgent(req *protov2.HeartbeatRequest) *model.Agent {
	a := &model.Agent{
		InstanceID:    string(req.InstanceId),
		AgentType:     req.AgentType,
		RunningStatus: req.RunningStatus,
		StartupTime:   req.StartupTime,
		SequenceNum:   req.SequenceNum,
		Capabilities:  req.Capabilities,
		LastHeartbeat: time.Now(),
	}
	if req.Attributes != nil {
		a.IP = string(req.Attributes.Ip)
		a.Hostname = string(req.Attributes.Hostname)
		a.Hostid = string(req.Attributes.Hostid)
		a.Version = string(req.Attributes.Version)
	}
	if b, err := json.Marshal(req.Tags); err == nil {
		a.TagsJSON = string(b)
	}
	return a
}

func protoTagsToModel(tags []*protov2.AgentGroupTag) []model.AgentGroupTag {
	out := make([]model.AgentGroupTag, 0, len(tags))
	for _, t := range tags {
		out = append(out, model.AgentGroupTag{TagName: t.Name, TagValue: t.Value})
	}
	return out
}

func indexConfigInfos(cfgs []*protov2.ConfigInfo) map[string]int64 {
	m := make(map[string]int64, len(cfgs))
	for _, c := range cfgs {
		m[c.Name] = c.Version
	}
	return m
}

func buildErrHBResp(reqID []byte, status int32, msg string) *protov2.HeartbeatResponse {
	return &protov2.HeartbeatResponse{
		RequestId: reqID,
		CommonResponse: &protov2.CommonResponse{
			Status:       status,
			ErrorMessage: []byte(msg),
		},
	}
}

func writeProtoResponse(w http.ResponseWriter, msg proto.Message) {
	raw, err := proto.Marshal(msg)
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", protoContentType)
	_, _ = w.Write(raw)
}

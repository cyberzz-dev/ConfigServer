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
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
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

	// Resolve configs for this agent based on its tags and attributes.
	match := model.AgentMatchContext{
		InstanceID: instanceID, // unique per agent process — preferred canary bucket key
		IP:         agent.IP,
		Hostid:     agent.Hostid,   // machine-stable OTel host.id; fallback bucket key
		Hostname:   agent.Hostname, // last-resort fallback when Hostid is not populated
		Version:    agent.Version,
		Tags:       protoTagsToModel(req.Tags),
	}
	pipelines, instances, onetimes, err := h.mgr.GetConfigsForAgent(ctx, match)
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
	// Names present in the server store regardless of delivery deadline. Used to decide
	// genuine cancellation: a command is cancelled ONLY when it has been deleted from the
	// store, NOT merely because its delivery deadline (expire_time) has passed.
	serverAllOnetimeNames := make(map[string]struct{}, len(onetimes))
	for _, oc := range onetimes {
		serverAllOnetimeNames[oc.Name] = struct{}{}
	}
	// Signal cancellation only for onetime configs the agent reports but the server has
	// genuinely deleted. expire_time is purely a DELIVERY deadline (it bounds how long the
	// server keeps offering the command to new agents); once delivered, the command's
	// lifetime is governed by the agent's execution timeout, so a passed delivery deadline
	// must NOT force-cancel an already-delivered (possibly still-running) onetime config.
	// Use expire_time=-1 as the cancel/delete sentinel (analogous to version=-1 for ConfigDetail).
	for name := range agentOnetime {
		if _, ok := serverAllOnetimeNames[name]; !ok {
			resp.OnetimePipelineConfigUpdates = append(resp.OnetimePipelineConfigUpdates, &protov2.CommandDetail{
				Name:       name,
				ExpireTime: -1,
			})
		}
	}
	for _, oc := range onetimes {
		// Skip delivery of commands past their delivery deadline: do not offer them to
		// agents that have not yet received them. Agents that already received the command
		// keep running it under their own execution timeout and are not cancelled here.
		if oc.ExpireTime > 0 && oc.ExpireTime < now {
			continue
		}
		// Inject the command's generation (CreatedAt) into the detail so that a
		// delete+recreate of a same-named command — which produces a new CreatedAt but may
		// carry identical user content — yields different delivered bytes. This defeats the
		// pure content-hash de-duplication below and lets the agent's execution layer treat
		// the recreated command as a fresh run instead of resuming the old checkpoint.
		detail := onetimeDetailWithGeneration(oc)
		// Deliver only if the agent hasn't reported it yet, or if the server has a
		// re-issued command with the same name but different content.  The agent
		// reports the FNV-1a hash of the delivered detail as the version field;
		// comparing it with onetimeContentHash(detail) detects same-name rewrites
		// (including same-content recreations, thanks to the injected generation).
		if agentVer, seen := agentOnetime[oc.Name]; seen && !fullState {
			if agentVer == onetimeContentHash(detail) {
				continue // agent already has this exact command
			}
			// Content differs: server re-issued the command; fall through to re-deliver.
		}
		resp.OnetimePipelineConfigUpdates = append(resp.OnetimePipelineConfigUpdates, &protov2.CommandDetail{
			Name:       oc.Name,
			Detail:     detail,
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
			Detail:     onetimeDetailWithGeneration(oc),
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

// onetimeGenerationKey is the key under the pipeline config's "global" section that
// carries the onetime command generation. The agent treats this as opaque config
// content: it is part of the bytes hashed for the delivery version, written to disk,
// and folded into the execution-layer inputs hash (see PipelineConfig.cpp), so that a
// changed generation forces a fresh rerun rather than a checkpoint resume.
const onetimeGenerationKey = "__onetime_generation__"

// onetimeDetailWithGeneration returns oc.Detail with the command's generation
// (CreatedAt in nanoseconds) injected into the "global" object under
// onetimeGenerationKey. A delete+recreate of a same-named command yields a new
// CreatedAt, so the returned bytes differ even when the user-supplied content is
// identical; this is what makes the agent re-deliver and re-run the command.
//
// The detail is decoded with UseNumber() so existing numeric values (e.g. large
// int64 parameters) are preserved verbatim and never degraded to float64. If the
// detail is not a JSON object or the generation is unavailable, the original bytes
// are returned unchanged (backward compatible with locally-created commands).
func onetimeDetailWithGeneration(oc *model.OnetimeCommand) []byte {
	gen := oc.CreatedAt.UnixNano()
	if gen <= 0 {
		return oc.Detail
	}
	dec := json.NewDecoder(bytes.NewReader(oc.Detail))
	dec.UseNumber()
	var root map[string]interface{}
	if err := dec.Decode(&root); err != nil || root == nil {
		return oc.Detail // not a JSON object; deliver as-is
	}
	global, ok := root["global"].(map[string]interface{})
	if !ok {
		global = map[string]interface{}{}
	}
	global[onetimeGenerationKey] = json.Number(strconv.FormatInt(gen, 10))
	root["global"] = global
	out, err := json.Marshal(root)
	if err != nil {
		return oc.Detail
	}
	return out
}

// onetimeContentHash computes the same FNV-1a 64-bit hash (masked to int64) as the
// C++ ComputeOnetimeConfigVersion in CommonConfigProvider.cpp.  It is used to
// decide whether the server's current command matches the version the agent already
// reported, so that a same-name re-issued onetime command is re-delivered.
func onetimeContentHash(detail []byte) int64 {
	const (
		offsetBasis = uint64(14695981039346656037)
		prime       = uint64(1099511628211)
	)
	h := offsetBasis
	for _, b := range detail {
		h ^= uint64(b)
		h *= prime
	}
	return int64(h & 0x7FFFFFFFFFFFFFFF)
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

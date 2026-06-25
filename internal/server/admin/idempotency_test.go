package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/alibaba/ilogtail/config_server/internal/cache"
	"github.com/alibaba/ilogtail/config_server/internal/config"
	"github.com/alibaba/ilogtail/config_server/internal/model"
	"github.com/alibaba/ilogtail/config_server/internal/store/gormdb"
)

func newTestAdmin(t *testing.T) (*http.ServeMux, *cache.Manager) {
	t.Helper()
	st, err := gormdb.New("sqlite", filepath.Join(t.TempDir(), "configserver.db"), gormdb.PoolConfig{})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	mgr := cache.New(st, nil, 1, time.Minute, time.Minute, false)
	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, NewAdminHandler(mgr, config.SMTPConfig{}))
	return mux, mgr
}

func adminReq(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func requireStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, want, rec.Body.String())
	}
}

func TestPipelineConfigWriteAPIsAreIdempotent(t *testing.T) {
	mux, mgr := newTestAdmin(t)
	body := `{"name":"p1","detail":"enable: true\ninputs: []\n"}`

	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/pipeline-configs", body), http.StatusCreated)
	cfg, err := mgr.GetPipelineConfig(t.Context(), "p1")
	if err != nil {
		t.Fatalf("get created config: %v", err)
	}
	version := cfg.Version

	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/pipeline-configs", body), http.StatusOK)
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/pipeline-configs", `{"name":"p1","detail":"enable: false\n"}`), http.StatusConflict)

	requireStatus(t, adminReq(t, mux, http.MethodPut, "/api/v1/pipeline-configs/p1", `{"detail":"enable: true\ninputs: []\n"}`), http.StatusOK)
	cfg, err = mgr.GetPipelineConfig(t.Context(), "p1")
	if err != nil {
		t.Fatalf("get unchanged config: %v", err)
	}
	if cfg.Version != version {
		t.Fatalf("same-content update changed version: got %d want %d", cfg.Version, version)
	}
	history, err := mgr.ListConfigHistory(t.Context(), model.ConfigTypePipeline, "p1")
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history entries after idempotent create/update = %d, want 1", len(history))
	}

	requireStatus(t, adminReq(t, mux, http.MethodDelete, "/api/v1/pipeline-configs/p1", ""), http.StatusOK)
	requireStatus(t, adminReq(t, mux, http.MethodDelete, "/api/v1/pipeline-configs/p1", ""), http.StatusOK)
	history, err = mgr.ListConfigHistory(t.Context(), model.ConfigTypePipeline, "p1")
	if err != nil {
		t.Fatalf("list history after delete: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history entries after duplicate delete = %d, want 2", len(history))
	}
	if history[0].Action != "delete" || len(history[0].Detail) == 0 {
		t.Fatalf("latest delete history should keep a rollback snapshot, got action=%q detail_len=%d", history[0].Action, len(history[0].Detail))
	}
}

func TestOnetimeCommandCreateAndDeleteAreIdempotent(t *testing.T) {
	mux, mgr := newTestAdmin(t)
	body := `{"name":"once","detail":"enable: true\n","expire_time":1893456000}`

	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/onetime-commands", body), http.StatusCreated)
	cmd, err := mgr.GetOnetimeCommand(t.Context(), "once")
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	version := cmd.Version

	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/onetime-commands", body), http.StatusOK)
	cmd, err = mgr.GetOnetimeCommand(t.Context(), "once")
	if err != nil {
		t.Fatalf("get duplicate command: %v", err)
	}
	if cmd.Version != version {
		t.Fatalf("duplicate create changed onetime version: got %d want %d", cmd.Version, version)
	}
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/onetime-commands", `{"name":"once","detail":"enable: false\n","expire_time":1893456000}`), http.StatusConflict)

	requireStatus(t, adminReq(t, mux, http.MethodDelete, "/api/v1/onetime-commands/once", ""), http.StatusOK)
	requireStatus(t, adminReq(t, mux, http.MethodDelete, "/api/v1/onetime-commands/once", ""), http.StatusOK)
	history, err := mgr.ListConfigHistory(t.Context(), model.ConfigTypeOnetime, "once")
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history entries after duplicate delete = %d, want 2", len(history))
	}
}

func TestCanaryAPIsAreIdempotent(t *testing.T) {
	mux, mgr := newTestAdmin(t)
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/pipeline-configs", `{"name":"p1","detail":"enable: true\n"}`), http.StatusCreated)

	canaryBody := `{"canary_detail":"enable: false\n","rollout_percent":25}`
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary", canaryBody), http.StatusCreated)
	cr, err := mgr.GetCanary(t.Context(), "p1", model.ConfigTypePipeline)
	if err != nil {
		t.Fatalf("get canary: %v", err)
	}
	canaryVersion := cr.CanaryVersion

	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary", canaryBody), http.StatusOK)
	requireStatus(t, adminReq(t, mux, http.MethodPut, "/api/v1/configs/pipeline/p1/canary", canaryBody), http.StatusOK)
	cr, err = mgr.GetCanary(t.Context(), "p1", model.ConfigTypePipeline)
	if err != nil {
		t.Fatalf("get unchanged canary: %v", err)
	}
	if cr.CanaryVersion != canaryVersion {
		t.Fatalf("same-content canary update changed version: got %d want %d", cr.CanaryVersion, canaryVersion)
	}
	requireStatus(t, adminReq(t, mux, http.MethodPut, "/api/v1/configs/pipeline/p1/canary/percent", `{"rollout_percent":25}`), http.StatusOK)

	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary/promote", ""), http.StatusOK)
	cfg, err := mgr.GetPipelineConfig(t.Context(), "p1")
	if err != nil {
		t.Fatalf("get promoted config: %v", err)
	}
	promotedVersion := cfg.Version
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary/promote", ""), http.StatusOK)
	cfg, err = mgr.GetPipelineConfig(t.Context(), "p1")
	if err != nil {
		t.Fatalf("get config after duplicate promote: %v", err)
	}
	if cfg.Version != promotedVersion {
		t.Fatalf("duplicate promote changed stable version: got %d want %d", cfg.Version, promotedVersion)
	}

	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary", `{"canary_detail":"enable: true\n","rollout_percent":10}`), http.StatusCreated)
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary/abort", ""), http.StatusOK)
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary/abort", ""), http.StatusOK)
}

func TestCanaryCreateConflictsWhenPayloadDiffers(t *testing.T) {
	mux, _ := newTestAdmin(t)
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/pipeline-configs", `{"name":"p1","detail":"enable: true\n"}`), http.StatusCreated)
	requireStatus(t, adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary", `{"canary_detail":"enable: false\n","rollout_percent":25}`), http.StatusCreated)

	rec := adminReq(t, mux, http.MethodPost, "/api/v1/configs/pipeline/p1/canary", `{"canary_detail":"enable: false\n","rollout_percent":50}`)
	requireStatus(t, rec, http.StatusConflict)
	var resp apiResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code == 0 {
		t.Fatalf("conflict response code should be non-zero")
	}
}

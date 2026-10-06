package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/sidecarsync"
)

type asyncSidecarStub struct {
	SidecarSyncManager
	err    error
	starts int
}

func (s *asyncSidecarStub) Start(context.Context) (sidecarsync.Config, error) {
	s.starts++
	return sidecarsync.Config{Enabled: true, Running: true}, s.err
}

func TestSyncRunEndpointAcknowledgesBackgroundWork(t *testing.T) {
	stub := &asyncSidecarStub{}
	router := NewRouter(Dependencies{Access: NewAccessGateService("", ""), SidecarSync: stub, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pan/sidecar-sync/run", nil))
	var config sidecarsync.Config
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &config) != nil || !config.Running || stub.starts != 1 {
		t.Fatalf("background run response: %d %s", rec.Code, rec.Body.String())
	}
	stub.err = domain.E(domain.KindBusy, "配置正在更新", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pan/sidecar-sync/run", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy run falsely accepted: %d", rec.Code)
	}
}

package emby

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/export"
)

type playbackRelay struct{}

func (playbackRelay) StreamURL(context.Context, string, string) (string, error) {
	return "https://cdn.example/video", nil
}
func (playbackRelay) Probe(context.Context, string, http.Header) (*http.Response, error) {
	return nil, nil
}

func TestPlaybackProxySettingsRestoreAndDoNotSaveOccupiedPort(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "emby") }))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	disabled := false
	mgr := export.NewManager(export.Config{EmbyDir: t.TempDir(), PublicURL: "http://miyabi:8080"})
	initial := Config{Enabled: true, ServerURL: upstream.URL, APIKey: "management-key", LocalDir: mgr.Config().EmbyDir, PublicURL: mgr.Config().PublicURL, SyncActors: &disabled}
	deps := Dependencies{ExportManager: mgr, PlaybackRelay: playbackRelay{}}
	service, err := NewService(t.Context(), store.Client, initial, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { service.Close() }()
	next := initial
	next.ProxyEnabled = true
	next.ProxyListen = address
	if err := service.UpdateConfig(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	active, _ := service.Config(t.Context())
	if !active.ProxyRunning || active.ProxyListen != address {
		t.Fatalf("not active: %+v", active)
	}
	saved, _, err := database.LoadSetting[Config](t.Context(), store.Client, SettingKey)
	if err != nil || saved.ProxyRunning || saved.ProxyError != "" {
		t.Fatal("runtime state persisted")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	next.ProxyListen = occupied.Addr().String()
	if err := service.UpdateConfig(t.Context(), next); err == nil {
		t.Fatal("occupied port save succeeded")
	}
	saved, _, _ = database.LoadSetting[Config](t.Context(), store.Client, SettingKey)
	if saved.ProxyListen != address {
		t.Fatal("failed save replaced persisted settings")
	}
	service.Close()
	service, err = NewService(t.Context(), store.Client, initial, deps)
	if err != nil {
		t.Fatal(err)
	}
	active, _ = service.Config(t.Context())
	if !active.ProxyRunning || active.ProxyListen != address {
		t.Fatal("proxy did not restore after restart")
	}
	active.Enabled = false
	if err := service.UpdateConfig(t.Context(), active); err != nil {
		t.Fatal(err)
	}
	active, _ = service.Config(t.Context())
	if active.ProxyRunning {
		t.Fatal("disabling Emby left proxy running")
	}
}

func TestProxyStartupFailureLeavesSettingsUsableAndCanRecover(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	address := occupied.Addr().String()
	disabled := false
	mgr := export.NewManager(export.Config{EmbyDir: t.TempDir(), PublicURL: "http://miyabi:8080"})
	cfg := Config{Enabled: true, ServerURL: "http://emby:8096", APIKey: "management-key", PublicURL: mgr.Config().PublicURL, LocalDir: mgr.Config().EmbyDir, SyncActors: &disabled, ProxyEnabled: true, ProxyListen: address}
	if err := database.SaveSetting(t.Context(), store.Client, SettingKey, cfg); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(t.Context(), store.Client, cfg, Dependencies{ExportManager: mgr, PlaybackRelay: playbackRelay{}})
	if err != nil {
		t.Fatalf("proxy failure prevented application initialization: %v", err)
	}
	defer service.Close()
	active, err := service.Config(t.Context())
	if err != nil || active.ProxyRunning || active.ProxyError == "" {
		t.Fatal("settings did not expose recoverable startup failure")
	}
	occupied.Close()
	if err := service.UpdateConfig(t.Context(), active); err != nil {
		t.Fatal(err)
	}
	active, _ = service.Config(t.Context())
	if !active.ProxyRunning || active.ProxyError != "" {
		t.Fatal("saved settings did not restart failed listener")
	}
}

func TestProxySettingsDoNotBlockNotificationsInsideWriterTransaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	disabled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer upstream.Close()
	cfg := Config{Enabled: true, ServerURL: upstream.URL, APIKey: "key", PublicURL: "http://miyabi:8080", SyncActors: &disabled}
	service, err := NewService(ctx, store.Client, cfg, Dependencies{ExportManager: export.NewManager(export.Config{})})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	tx, err := store.Client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := database.SaveSetting(ctx, tx.Client(), "hold-writer", "value"); err != nil {
		t.Fatal(err)
	}
	persisting := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- service.playbackProxy.Configure(proxyConfig(cfg), func() error {
			close(persisting)
			return database.SaveSetting(ctx, store.Client, SettingKey, cfg)
		})
	}()
	<-persisting
	if err := service.NotifyUpdatedTx(ctx, tx, t.TempDir()); err != nil {
		t.Fatalf("proxy settings blocked library commit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

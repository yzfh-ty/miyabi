package sidecarsync

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/drive"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/export"
	"github.com/ppxb/miyabi/internal/pan"
)

type blockedSyncClient struct {
	*syncClient
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
	failure error
}

func (c *blockedSyncClient) List(ctx context.Context, token, id string, offset, limit int) (pan.FilePage, error) {
	c.calls.Add(1)
	c.once.Do(func() { close(c.entered) })
	select {
	case <-ctx.Done():
		return pan.FilePage{}, ctx.Err()
	case <-c.release:
	}
	if c.failure != nil {
		return pan.FilePage{}, c.failure
	}
	return c.syncClient.List(ctx, token, id, offset, limit)
}

func asyncFixture(t *testing.T) (*Service, *blockedSyncClient) {
	t.Helper()
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for key, value := range map[string]any{
		"pan.credentials":       pan.Tokens{AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour)},
		"pan.library_directory": map[string]string{"account_id": "100", "id": "10", "name": "Media"},
	} {
		if err := database.SaveSetting(t.Context(), store.Client, key, value); err != nil {
			t.Fatal(err)
		}
	}
	client := &blockedSyncClient{syncClient: &syncClient{entries: map[string][]pan.File{"10": {}}}, entered: make(chan struct{}), release: make(chan struct{})}
	d, err := drive.NewWithClient(t.Context(), store.Client, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	svc := New(store.Client, d, export.NewManager(export.Config{EmbyDir: t.TempDir(), PublicURL: "http://miyabi:8080"}), nil, nil)
	t.Cleanup(svc.Close)
	if err := database.SaveSetting(t.Context(), store.Client, configKey, Config{Enabled: true, AccountID: "100", ParentID: "10", IntervalMinutes: 30}); err != nil {
		t.Fatal(err)
	}
	return svc, client
}

func TestStartReturnsBeforeWorkAndSurvivesRequestCancellation(t *testing.T) {
	svc, client := asyncFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	accepted, err := svc.Start(ctx)
	if err != nil || !accepted.Running {
		t.Fatalf("not accepted: %+v %v", accepted, err)
	}
	cancel()
	select {
	case <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("background job did not start")
	}
	status, err := svc.Config(t.Context())
	if err != nil || !status.Running || status.LastRunAt != nil {
		t.Fatal("request disconnect canceled work or prematurely marked completion")
	}
	duplicate, err := svc.Start(t.Context())
	if err != nil || !duplicate.Running || client.calls.Load() != 1 {
		t.Fatal("duplicate submission started another run")
	}
	if _, err := svc.Update(t.Context(), Update{IntervalMinutes: 30}); !domain.IsKind(err, domain.KindBusy) {
		t.Fatalf("config request did not fail fast: %v", err)
	}
	close(client.release)
	if err := svc.mu.Lock(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc.mu.Unlock()
	status, err = svc.Config(t.Context())
	if err != nil || status.Running || status.LastResult != "success" || status.LastRunAt == nil {
		t.Fatalf("final result not visible: %+v %v", status, err)
	}
}

func TestBackgroundFailureIsReportedByStatusNotStartResponse(t *testing.T) {
	svc, client := asyncFixture(t)
	client.failure = context.DeadlineExceeded
	accepted, err := svc.Start(t.Context())
	if err != nil || !accepted.Running {
		t.Fatal("start waited for or misreported execution result")
	}
	close(client.release)
	if err := svc.mu.Lock(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc.mu.Unlock()
	status, err := svc.Config(t.Context())
	if err != nil || status.Running || status.LastResult == "success" || len(status.Errors) == 0 {
		t.Fatalf("background failure lost: %+v %v", status, err)
	}
}

func TestCloseCancelsAndJoinsBackgroundWork(t *testing.T) {
	svc, client := asyncFixture(t)
	if _, err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-client.entered
	closed := make(chan struct{})
	go func() { svc.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown retained background job")
	}
	if svc.running.Load() {
		t.Fatal("shutdown still reports active work")
	}
	if _, err := svc.Start(t.Context()); err == nil {
		t.Fatal("closed service restarted")
	}
}

func TestStatusReadOverlappingCompletionRequestsOneMorePoll(t *testing.T) {
	svc, client := asyncFixture(t)
	if _, err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-client.entered
	read := make(chan struct{})
	resume := make(chan struct{})
	var block atomic.Bool
	block.Store(true)
	svc.db.Setting.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			value, err := next.Query(ctx, query)
			if block.CompareAndSwap(true, false) {
				close(read)
				<-resume
			}
			return value, err
		})
	}))
	result := make(chan Config, 1)
	go func() { config, _ := svc.Config(t.Context()); result <- config }()
	<-read
	close(client.release)
	if err := svc.mu.Lock(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc.mu.Unlock()
	close(resume)
	if snapshot := <-result; !snapshot.Running {
		t.Fatal("old snapshot stopped polling before receiving completion")
	}
	final, err := svc.Config(t.Context())
	if err != nil || final.Running || final.LastResult != "success" || final.LastRunAt == nil {
		t.Fatalf("completion missing: %+v %v", final, err)
	}
}

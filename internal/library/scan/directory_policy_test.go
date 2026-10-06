package scan

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent/task"
	"github.com/ppxb/miyabi/internal/export"
	"github.com/ppxb/miyabi/internal/library/scrape"
	"github.com/ppxb/miyabi/internal/tasks"
)

func TestReconcileSkipsMetadataSyncDirectories(t *testing.T) {
	for _, completed := range []int{0, 1} {
		run := reconcileFixture(t, completed, 1-completed)
		ctx := t.Context()
		film := run.scanner.db.Movie.Query().OnlyX(ctx)
		source := run.payload.Source
		if err := database.SaveSetting(ctx, run.scanner.db, database.PanDirectorySettingsKey, domain.DirectoryPolicy{
			AccountID: source.AccountID, ParentID: source.Directory.ID,
			DownloadDirectory: domain.LibraryDirectory{ID: "downloads", Path: "Downloads"},
		}); err != nil {
			t.Fatal(err)
		}
		body, err := tasks.EncodePayload(scrape.MetadataPayload{Source: source, MovieID: film.ID, Code: film.Code, ScanTaskID: 900})
		if err != nil {
			t.Fatal(err)
		}
		original := run.scanner.db.Task.Create().SetType("scrape").SetResourceKey("movie:" + strconv.Itoa(film.ID)).SetPayload(body).SaveX(ctx)
		if err := run.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if got := run.scanner.db.Task.Query().Where(task.TypeEQ("scrape")).CountX(ctx); got != 1 || len(run.payload.ReusedTasks) != 0 {
			t.Fatalf("metadata-only movie scheduled or reused scraping: jobs=%d reused=%v", got, run.payload.ReusedTasks)
		}
		if got := run.scanner.db.Task.GetX(ctx, original.ID); string(got.Payload) != string(body) {
			t.Fatal("reconciliation changed the existing task")
		}
		if _, err := os.Stat(scrape.EmbyMovieDir(run.scanner.exportMgr.Config().EmbyDir, film.Code)); !os.IsNotExist(err) {
			t.Fatalf("metadata-only movie exported sidecars: %v", err)
		}
	}
}

func TestChangingToMetadataSyncPreservesLocalExportedFiles(t *testing.T) {
	ctx := t.Context()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source := domain.LibrarySource{AccountID: "account", Directory: domain.LibraryDirectory{ID: "root", Path: "/Media"}}
	policy := domain.DirectoryPolicy{AccountID: source.AccountID, ParentID: source.Directory.ID,
		DownloadDirectory: domain.LibraryDirectory{ID: "downloads", Path: "Downloads"}}
	if err := database.SaveSetting(ctx, store.Client, database.PanDirectorySettingsKey, policy); err != nil {
		t.Fatal(err)
	}
	film := store.Client.Movie.Create().SetCode("ABP-001").SaveX(ctx)
	store.Client.File.Create().SetFileID("101").SetName("ABP-001.mp4").SetSize(1 << 30).
		SetParentID("existing").SetAccountID(source.AccountID).SetRootID(source.Directory.ID).
		SetPath("/Media/Existing/ABP-001.mp4").SetScanID("previous").SetMovie(film).ExecX(ctx)
	root := t.TempDir()
	dir := export.EmbyMovieDir(root, film.Code)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "ABP-001.nfo")
	if err := os.WriteFile(filename, []byte("existing metadata"), 0644); err != nil {
		t.Fatal(err)
	}
	job := store.Client.Task.Create().SetType("scan").SaveX(ctx)
	payload := domain.ScanPayload{Source: source, ScanID: "current"}
	if err := ReconcileScan(ctx, store.Client, job.ID, &payload, nil, nil, export.Config{EmbyDir: root}, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filename)
	if err != nil || string(body) != "existing metadata" {
		t.Fatalf("changing directory mode removed local metadata: %q, %v", body, err)
	}
}

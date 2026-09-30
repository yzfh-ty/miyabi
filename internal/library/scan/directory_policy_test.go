package scan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/export"
)

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

package scrape

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/export"
	"github.com/ppxb/miyabi/internal/nfo"
)

func TestCorrectedExportCleanupPreservesCustomFilesAndCanReplay(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := &Service{db: store.Client}
	record := store.Client.Movie.Create().SetCode("IPX-123").SetMetadataSnapshot(&domain.MetadataSnapshot{Code: "ABP-001"}).SaveX(t.Context())
	root := t.TempDir()
	oldDir, newDir := export.EmbyMovieDir(root, "ABP-001"), export.EmbyMovieDir(root, "IPX-123")
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	generated := []string{"ABP-001.strm", "ABP-001-cd1.strm", "ABP-001-cd2.strm", "ABP-001-cd10.strm", "ABP-001.nfo", "poster.jpg", "fanart.jpg"}
	retained := []string{
		"original.mp4", "notes.txt", "custom.nfo", "bonus.strm", "custom-upper.NFO", "bonus-upper.STRM",
		"ABP-001-extra.nfo", "ABP-001-bonus.strm", "ABP-0010.strm",
		"ABP-001-cd0.strm", "ABP-001-cd01.strm", "ABP-001-cd.strm",
		"ABP-001-cdx.strm", "ABP-001-cd1-extra.strm", "ABP-001-cd1.nfo",
	}
	for _, name := range append(generated, retained...) {
		if err := os.WriteFile(filepath.Join(oldDir, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(oldDir, "ABP-001-cd3.strm", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(nested), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("nested file"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "IPX-123.strm"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		path, err := service.removePreviousExport(t.Context(), root, record, record.Code)
		if err != nil || path != oldDir {
			t.Fatalf("cleanup = %q, %v", path, err)
		}
	}
	for _, name := range generated {
		if _, err := os.Stat(filepath.Join(oldDir, name)); !os.IsNotExist(err) {
			t.Fatalf("old export remains: %s", name)
		}
	}
	for _, name := range retained {
		if body, err := os.ReadFile(filepath.Join(oldDir, name)); err != nil || string(body) != name {
			t.Fatalf("custom file changed: %s, %q, %v", name, body, err)
		}
	}
	for _, path := range []string{nested, filepath.Join(newDir, "IPX-123.strm")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("deleted retained file: %s", path)
		}
	}
	store.Client.Movie.Create().SetCode("ABP-001").ExecX(t.Context())
	if err := os.WriteFile(filepath.Join(oldDir, "ABP-001.strm"), []byte("another movie"), 0644); err != nil {
		t.Fatal(err)
	}
	if path, err := service.removePreviousExport(t.Context(), root, record, record.Code); err != nil || path != "" {
		t.Fatal("deleted another movie's export")
	}
	if _, err := os.Stat(filepath.Join(oldDir, "ABP-001.strm")); err != nil {
		t.Fatal(err)
	}
}

func TestCorrectedExportCleanupUsesEncodedFileStem(t *testing.T) {
	store, err := database.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := &Service{db: store.Client}
	const oldCode = "ABC.123/特別 1"
	record := store.Client.Movie.Create().SetCode("IPX-123").
		SetMetadataSnapshot(&domain.MetadataSnapshot{Code: oldCode}).SaveX(t.Context())
	root := t.TempDir()
	dir := export.EmbyMovieDir(root, oldCode)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	stem := nfo.FileStem(oldCode)
	generated := []string{stem + ".nfo", stem + ".strm", stem + "-cd12.strm"}
	custom := nfo.FileStem("ABCx123/特別 1") + ".nfo"
	for _, name := range append(generated, custom) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.removePreviousExport(t.Context(), root, record, record.Code); err != nil {
		t.Fatal(err)
	}
	for _, name := range generated {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("encoded export remains: %s, %v", name, err)
		}
	}
	if body, err := os.ReadFile(filepath.Join(dir, custom)); err != nil || string(body) != custom {
		t.Fatalf("similarly named custom file changed: %q, %v", body, err)
	}
}

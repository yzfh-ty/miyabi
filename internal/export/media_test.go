package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ppxb/miyabi/internal/nfo"
	"github.com/ppxb/miyabi/internal/pan"
)

func TestWriteMedia_WritesSTRMLast(t *testing.T) {
	tempDir := t.TempDir()
	embyDir := filepath.Join(tempDir, "emby")

	doc := nfo.Movie{
		Code:  "SSIS-999",
		Title: "Test Write Order",
	}
	videos := []pan.File{
		{ID: "vid-1", Name: "SSIS-999.mp4"},
	}
	poster := []byte("poster data")
	fanart := []byte("fanart data")

	_, err := WriteMedia(Config{EmbyDir: embyDir, PublicURL: "http://127.0.0.1:8080", STRMToken: ""}, "SSIS-999", doc, videos, poster, fanart)
	if err != nil {
		t.Fatalf("WriteMedia failed: %v", err)
	}

	movieDir := filepath.Join(embyDir, ManagedDirectory, "SSIS", "SSIS-999")
	posterStat, err := os.Stat(filepath.Join(movieDir, "poster.jpg"))
	if err != nil {
		t.Fatalf("poster not found: %v", err)
	}
	nfoStat, err := os.Stat(filepath.Join(movieDir, "SSIS-999.nfo"))
	if err != nil {
		t.Fatalf("nfo not found: %v", err)
	}
	strmStat, err := os.Stat(filepath.Join(movieDir, "SSIS-999.strm"))
	if err != nil {
		t.Fatalf("strm not found: %v", err)
	}

	// STRM mod time must not be before poster or nfo
	if strmStat.ModTime().Before(posterStat.ModTime()) {
		t.Errorf("expected strm to be written after or equal to poster: strm=%v, poster=%v", strmStat.ModTime(), posterStat.ModTime())
	}
	if strmStat.ModTime().Before(nfoStat.ModTime()) {
		t.Errorf("expected strm to be written after or equal to nfo: strm=%v, nfo=%v", strmStat.ModTime(), nfoStat.ModTime())
	}
}

func TestNaturalCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"cd1", "cd2", -1},
		{"cd2", "cd10", -1},
		{"cd10", "cd2", 1},
		{"CD1", "cd1", 0},
		{"cd01", "cd1", 0},
		{"cd01", "cd2", -1},
		{"abc", "abc", 0},
		{"abc", "abd", -1},
		{"abd", "abc", 1},
		{"SSIS-456-CD1.mp4", "SSIS-456-CD2.mp4", -1},
		{"SSIS-456-CD2.mp4", "SSIS-456-CD10.mp4", -1},
		{"file10a", "file2b", 1},
	}

	for _, tc := range cases {
		got := naturalCompare(tc.a, tc.b)
		if (tc.want < 0 && got >= 0) || (tc.want > 0 && got <= 0) || (tc.want == 0 && got != 0) {
			t.Errorf("naturalCompare(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSortFiles(t *testing.T) {
	files := []pan.File{
		{ID: "3", Name: "SSIS-456-cd10.mp4"},
		{ID: "2", Name: "SSIS-456-CD2.mp4"},
		{ID: "1", Name: "SSIS-456-cd1.mp4"},
	}

	sortFiles(files)

	if files[0].ID != "1" || files[1].ID != "2" || files[2].ID != "3" {
		t.Fatalf("unexpected sort order: %+v", files)
	}
}

func TestWriteMedia_CleansObsoleteSTRM(t *testing.T) {
	tempDir := t.TempDir()
	embyDir := filepath.Join(tempDir, "emby")
	doc := nfo.Movie{Code: "MIDE-123", Title: "Clean Test"}

	// Step 1: Export single video -> MIDE-123.strm
	singleVideo := []pan.File{{ID: "v1", Name: "MIDE-123.mp4"}}
	if _, err := WriteMedia(Config{EmbyDir: embyDir, PublicURL: "http://localhost:8080", STRMToken: "tok"}, "MIDE-123", doc, singleVideo, nil, nil); err != nil {
		t.Fatalf("first export failed: %v", err)
	}

	movieDir := filepath.Join(embyDir, ManagedDirectory, "MIDE", "MIDE-123")
	if _, err := os.Stat(filepath.Join(movieDir, "MIDE-123.strm")); err != nil {
		t.Fatalf("expected MIDE-123.strm to exist: %v", err)
	}

	// Step 2: Export two disc videos -> MIDE-123-cd1.strm, MIDE-123-cd2.strm. Old MIDE-123.strm should be removed.
	twoVideos := []pan.File{
		{ID: "v1", Name: "MIDE-123-CD1.mp4"},
		{ID: "v2", Name: "MIDE-123-CD2.mp4"},
	}
	if _, err := WriteMedia(Config{EmbyDir: embyDir, PublicURL: "http://localhost:8080", STRMToken: "tok"}, "MIDE-123", doc, twoVideos, nil, nil); err != nil {
		t.Fatalf("second export failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(movieDir, "MIDE-123.strm")); !os.IsNotExist(err) {
		t.Fatalf("expected obsolete MIDE-123.strm to be removed, got err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(movieDir, "MIDE-123-cd1.strm")); err != nil {
		t.Fatalf("expected MIDE-123-cd1.strm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(movieDir, "MIDE-123-cd2.strm")); err != nil {
		t.Fatalf("expected MIDE-123-cd2.strm: %v", err)
	}

	// Step 3: Export back to single video -> MIDE-123.strm. cd1 and cd2 strm should be removed.
	if _, err := WriteMedia(Config{EmbyDir: embyDir, PublicURL: "http://localhost:8080", STRMToken: "tok"}, "MIDE-123", doc, singleVideo, nil, nil); err != nil {
		t.Fatalf("third export failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(movieDir, "MIDE-123.strm")); err != nil {
		t.Fatalf("expected MIDE-123.strm to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(movieDir, "MIDE-123-cd1.strm")); !os.IsNotExist(err) {
		t.Fatalf("expected obsolete MIDE-123-cd1.strm to be removed")
	}
	if _, err := os.Stat(filepath.Join(movieDir, "MIDE-123-cd2.strm")); !os.IsNotExist(err) {
		t.Fatalf("expected obsolete MIDE-123-cd2.strm to be removed")
	}
}

func TestWriteMedia_MultiVideoOrder(t *testing.T) {
	tempDir := t.TempDir()
	embyDir := filepath.Join(tempDir, "emby")
	doc := nfo.Movie{Code: "STARS-001", Title: "Multi Order Test"}

	// Pass videos in reverse order
	videos := []pan.File{
		{ID: "vid-part2", Name: "STARS-001-cd2.mp4"},
		{ID: "vid-part1", Name: "STARS-001-cd1.mp4"},
	}

	if _, err := WriteMedia(Config{EmbyDir: embyDir, PublicURL: "http://localhost:8080", STRMToken: "tok"}, "STARS-001", doc, videos, nil, nil); err != nil {
		t.Fatalf("WriteMedia failed: %v", err)
	}

	movieDir := filepath.Join(embyDir, ManagedDirectory, "STARS", "STARS-001")
	cd1Bytes, err := os.ReadFile(filepath.Join(movieDir, "STARS-001-cd1.strm"))
	if err != nil {
		t.Fatalf("cd1 strm missing: %v", err)
	}
	if !strings.Contains(string(cd1Bytes), "vid-part1") {
		t.Fatalf("expected cd1 strm to contain vid-part1, got %s", string(cd1Bytes))
	}

	cd2Bytes, err := os.ReadFile(filepath.Join(movieDir, "STARS-001-cd2.strm"))
	if err != nil {
		t.Fatalf("cd2 strm missing: %v", err)
	}
	if !strings.Contains(string(cd2Bytes), "vid-part2") {
		t.Fatalf("expected cd2 strm to contain vid-part2, got %s", string(cd2Bytes))
	}
}

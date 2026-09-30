package sidecarsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/drive"
	"github.com/ppxb/miyabi/internal/export"
	"github.com/ppxb/miyabi/internal/pan"
	"github.com/ppxb/miyabi/internal/strm"
)

type syncClient struct {
	drive.Client
	entries   map[string][]pan.File
	bodies    map[string][]byte
	readCalls int
}

func (*syncClient) Close() {}

func (*syncClient) Account(context.Context, string) (pan.Account, error) {
	return pan.Account{ID: "100"}, nil
}

func (c *syncClient) List(_ context.Context, _, id string, offset, _ int) (pan.FilePage, error) {
	entries, ok := c.entries[id]
	if !ok {
		return pan.FilePage{}, fmt.Errorf("unexpected directory %s", id)
	}
	end := min(offset+2, len(entries))
	return pan.FilePage{
		Files: entries[offset:end], Total: len(entries), HasMore: end < len(entries),
		Path: []pan.Directory{{ID: "10", Name: "Media"}},
	}, nil
}

func (c *syncClient) Info(_ context.Context, _, id string) (pan.FileInfo, error) {
	for _, entries := range c.entries {
		for _, entry := range entries {
			if entry.ID == id {
				return pan.FileInfo{File: entry, Path: []pan.Directory{{ID: "10", Name: "Media"}}}, nil
			}
		}
	}
	return pan.FileInfo{}, fmt.Errorf("unexpected file %s", id)
}

func (c *syncClient) ReadMetadata(_ context.Context, _, pickCode string, _ int64) ([]byte, error) {
	c.readCalls++
	body, ok := c.bodies[pickCode]
	if !ok {
		return nil, fmt.Errorf("unexpected metadata download %s", pickCode)
	}
	return body, nil
}

func (*syncClient) DownloadURL(_ context.Context, _, pickCode, userAgent string) (string, error) {
	if pickCode != "video-pick" || userAgent != "Emby" {
		return "", fmt.Errorf("unexpected playback request %s %s", pickCode, userAgent)
	}
	return "https://cdn.example/video.mkv", nil
}

func TestSyncGeneratesPlayableSTRMsWithOriginalMetadataAndPaths(t *testing.T) {
	ctx := t.Context()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for key, value := range map[string]any{
		"pan.credentials":       pan.Tokens{AccessToken: "access", ExpiresAt: time.Now().Add(time.Hour)},
		"pan.library_directory": map[string]string{"account_id": "100", "id": "10", "name": "Media"},
	} {
		if err := database.SaveSetting(ctx, store.Client, key, value); err != nil {
			t.Fatal(err)
		}
	}
	client := &syncClient{
		entries: map[string][]pan.File{
			"10": {
				{ID: "20", ParentID: "10", Name: "精选", IsDirectory: true},
				{ID: "99", ParentID: "10", Name: "未选择", IsDirectory: true},
			},
			"20": {
				{ID: "101", Name: "旅行 2026.MKV", PickCode: "video-pick", Size: 1 << 30},
				{ID: "30", Name: "多集", IsDirectory: true},
				{ID: "103", Name: "旅行 2026.nfo", PickCode: "nfo-pick"},
				{ID: "104", Name: "poster.jpg", PickCode: "poster-pick"},
				{ID: "105", Name: "kept.mp4"},
				{ID: "106", Name: "remote.strm", PickCode: "remote-strm"},
				{ID: "107", Name: "notes.txt", PickCode: "text"},
			},
			"30": {
				{ID: "201", Name: "part 2.mp4", Size: 3},
				{ID: "202", Name: "part 2.nfo", PickCode: "part-nfo"},
			},
		},
		bodies: map[string][]byte{
			"nfo-pick": []byte("<movie><title>原始标题</title></movie>\n"),
			"part-nfo": []byte("<movie><title>第二集</title></movie>\n"),
		},
	}
	d, err := drive.NewWithClient(ctx, store.Client, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	mgr := export.NewManager(export.Config{PublicURL: "http://media.example/", STRMToken: "play token"})
	svc := New(store.Client, d, mgr, nil)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "精选"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"poster.jpg": "local poster", "kept.strm": "https://existing.example/video\n"} {
		if err := os.WriteFile(filepath.Join(root, "精选", name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Update(ctx, Update{
		Enabled: true, Destination: root, IntervalMinutes: 30,
		ChildDirectories: []Directory{{ID: "20"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	wantFiles := map[string]string{
		"精选/旅行 2026.strm":   "http://media.example/api/strm/play/101?token=play+token\n",
		"精选/旅行 2026.nfo":    string(client.bodies["nfo-pick"]),
		"精选/poster.jpg":     "local poster",
		"精选/kept.strm":      "https://existing.example/video\n",
		"精选/多集/part 2.strm": "http://media.example/api/strm/play/201?token=play+token\n",
		"精选/多集/part 2.nfo":  string(client.bodies["part-nfo"]),
	}
	assertFiles := func() {
		t.Helper()
		seen := 0
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			want, ok := wantFiles[filepath.ToSlash(rel)]
			if !ok {
				t.Errorf("unexpected local file %s", rel)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != want {
				t.Errorf("%s = %q, %v; want %q", rel, body, err, want)
			}
			seen++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if seen != len(wantFiles) {
			t.Fatalf("local files = %d; want %d", seen, len(wantFiles))
		}
	}
	assertFiles()
	config, err := svc.Config(ctx)
	if err != nil || config.FilesDownloaded != 2 || config.FilesGenerated != 2 || config.FilesSkipped != 2 || config.LastResult != "success" {
		t.Fatalf("sync result = %+v, %v", config, err)
	}
	if store.Client.Task.Query().CountX(ctx) != 0 || store.Client.Movie.Query().CountX(ctx) != 0 || store.Client.File.Query().CountX(ctx) != 0 {
		t.Fatal("sidecar sync must generate STRMs without scraping or indexing movies")
	}
	address, err := strm.New(store.Client, d).StreamURL(ctx, "101", "Emby")
	if err != nil || address != "https://cdn.example/video.mkv" {
		t.Fatalf("unindexed STRM playback = %q, %v", address, err)
	}
	if err := svc.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	assertFiles()
	config, err = svc.Config(ctx)
	if err != nil || config.FilesDownloaded != 0 || config.FilesGenerated != 0 || config.FilesSkipped != 6 || client.readCalls != 2 {
		t.Fatalf("repeat sync result = %+v, %v; metadata reads = %d", config, err, client.readCalls)
	}
}

func TestGenerateSTRMUsesCurrentExportConfigAndRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	mgr := export.NewManager(export.Config{PublicURL: "http://old.example", STRMToken: "old"})
	svc := New(nil, nil, mgr, nil)
	mgr.Set(export.Config{PublicURL: "http://current.example", STRMToken: "new"})
	generated, err := svc.generateSTRM(root, filepath.Join("movies", "video.mp4"), "123")
	if err != nil || !generated {
		t.Fatalf("generate STRM = %t, %v", generated, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "movies", "video.strm"))
	if err != nil || string(body) != "http://current.example/api/strm/play/123?token=new\n" {
		t.Fatalf("STRM content = %q, %v", body, err)
	}
	if _, err := svc.generateSTRM(root, filepath.Join("..", "escape.mp4"), "456"); err == nil {
		t.Fatal("path outside sync root was accepted")
	}
}

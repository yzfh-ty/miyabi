package database

import (
	"testing"

	"github.com/ppxb/miyabi/internal/domain"
)

func TestDirectoryPolicyDoesNotScrapeUnconfiguredOrOtherMounts(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source := domain.LibrarySource{AccountID: "account", Directory: domain.LibraryDirectory{ID: "root", Path: "/Media"}}
	policy, err := LoadDirectoryPolicy(ctx, store.Client, source)
	if err != nil || policy.ShouldScrape(source, "root", "/Media/video.mp4") {
		t.Fatalf("unconfigured policy = %+v, %v", policy, err)
	}
	saved := domain.DirectoryPolicy{AccountID: source.AccountID, ParentID: source.Directory.ID, DownloadDirectory: source.Directory}
	if err := SaveSetting(ctx, store.Client, PanDirectorySettingsKey, saved); err != nil {
		t.Fatal(err)
	}
	policy, err = LoadDirectoryPolicy(ctx, store.Client, source)
	if err != nil || !policy.ShouldScrape(source, "root", "/Media/video.mp4") {
		t.Fatalf("explicit root download policy = %+v, %v", policy, err)
	}
	source.Directory.ID = "other-root"
	policy, err = LoadDirectoryPolicy(ctx, store.Client, source)
	if err != nil || policy.ShouldScrape(source, "other-root", "/Media/video.mp4") {
		t.Fatalf("policy leaked across mounts = %+v, %v", policy, err)
	}
}

package domain

import (
	"encoding/json"
	"testing"
)

func TestDirectoryPolicyDefaultsToTheDownloadSubtree(t *testing.T) {
	source := LibrarySource{AccountID: "account", Directory: LibraryDirectory{ID: "root", Path: "/Media"}}
	policy := DirectoryPolicy{DownloadDirectory: LibraryDirectory{ID: "downloads", Path: "Downloads"}}
	for _, tc := range []struct {
		id, path string
		want     bool
	}{
		{"downloads", "/Media/Downloads", true},
		{"nested", "/Media/Downloads/Series/video.mp4", true},
		{"other", "/Media/Downloads-old/video.mp4", false},
		{"other", "/Media/Already scraped/video.mp4", false},
		{"root", "/Media/video.mp4", false},
	} {
		if got := policy.ShouldScrape(source, tc.id, tc.path); got != tc.want {
			t.Errorf("ShouldScrape(%q, %q) = %t; want %t", tc.id, tc.path, got, tc.want)
		}
	}
	if (DirectoryPolicy{}).ShouldScrape(source, "other", "/Media/video.mp4") {
		t.Fatal("an unselected directory defaults to scraping")
	}
}

func TestDirectoryPolicyExplicitModesAndLegacySyncDirectories(t *testing.T) {
	source := LibrarySource{Directory: LibraryDirectory{ID: "root", Path: "/Media"}}
	var policy DirectoryPolicy
	if err := json.Unmarshal([]byte(`{"download_directory":{"id":"root"},"child_directories":[{"id":"existing","name":"Existing","path":"Existing"},{"id":"manual","path":"Manual","mode":"scrape"}]}`), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.ShouldScrape(source, "nested", "/Media/Existing/Series/video.mp4") {
		t.Fatal("an existing metadata sync directory was changed to scraping")
	}
	policy.DownloadDirectory = LibraryDirectory{ID: "downloads", Path: "Downloads"}
	if !policy.ShouldScrape(source, "nested", "/Media/Manual/Series/video.mp4") {
		t.Fatal("an explicit scrape rule did not apply to its descendants")
	}
	if policy.ShouldScrape(source, "other", "/Media/Existing-old/video.mp4") {
		t.Fatal("a rule matched a directory name prefix instead of a subtree")
	}
}

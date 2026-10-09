package offline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/pan"
)

func TestOfflineUsesSelectedDownloadDirectoryAndRetainsLibraryScope(t *testing.T) {
	service, client := offlineAddFixture(t)
	ctx := t.Context()
	source := *service.drive.Source()
	policy := domain.DirectoryPolicy{
		AccountID: source.AccountID, ParentID: source.Directory.ID,
		DownloadDirectory: domain.LibraryDirectory{ID: "downloads", Name: "Downloads", Path: "Downloads"},
	}
	if err := database.SaveSetting(ctx, service.database, database.PanDirectorySettingsKey, policy); err != nil {
		t.Fatal(err)
	}
	client.info = func(_ context.Context, _, id string) (pan.FileInfo, error) {
		return pan.FileInfo{
			File: pan.File{ID: id, Name: "Downloads", ParentID: source.Directory.ID, IsDirectory: true},
			Path: []pan.Directory{{ID: source.Directory.ID, Name: source.Directory.Name}},
		}, nil
	}
	client.addOffline = func(_ context.Context, _, _, directoryID string) (string, error) {
		if directoryID != "downloads" {
			t.Fatalf("download destination = %s; want downloads", directoryID)
		}
		return offlineHashA, nil
	}
	added, err := service.Add(ctx, "fixture-movie", offlineHashA)
	if err != nil {
		t.Fatal(err)
	}
	if added.DirectoryID != source.Directory.ID {
		t.Fatal("download destination replaced the library scope used for task tracking")
	}
	activity, err := service.Activity(ctx)
	if err != nil || len(activity.Tasks) != 1 || activity.Tasks[0].TaskID != added.TaskID {
		t.Fatalf("selected-directory download is missing from activity: %+v, %v", activity, err)
	}
}

func TestRecoveryKeepsSelectedDownloadDirectory(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "submission timeout"}[timeout], func(t *testing.T) {
			r := newRecoveryRig(t)
			ctx := t.Context()
			source := *r.s.drive.Source()
			policy := domain.DirectoryPolicy{
				AccountID: source.AccountID, ParentID: source.Directory.ID,
				DownloadDirectory: domain.LibraryDirectory{ID: "downloads", Name: "Downloads", Path: "Downloads"},
			}
			if err := database.SaveSetting(ctx, r.s.database, database.PanDirectorySettingsKey, policy); err != nil {
				t.Fatal(err)
			}
			r.client.info = func(_ context.Context, _, id string) (pan.FileInfo, error) {
				return pan.FileInfo{
					File: pan.File{ID: id, Name: "Downloads", ParentID: source.Directory.ID, IsDirectory: true},
					Path: []pan.Directory{{ID: source.Directory.ID, Name: source.Directory.Name}},
				}, nil
			}
			add := r.client.addOffline
			r.client.addOffline = func(ctx context.Context, token, uri, directory string) (string, error) {
				if directory != "downloads" {
					t.Fatalf("recovery destination = %s; want downloads", directory)
				}
				hash, err := add(ctx, token, uri, directory)
				if timeout {
					return "", context.DeadlineExceeded
				}
				return hash, err
			}
			if timeout {
				if _, err := r.s.Add(ctx, "fixture-movie", offlineHashA); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				record := r.s.database.OfflineDownload.Query().OnlyX(ctx)
				r.now = record.Recovery.RetryAt
				r.poll(0)
				if len(r.events) != 1 || r.s.database.OfflineDownload.GetX(ctx, record.ID).Recovery.Action != "" {
					t.Fatal("recovery did not reuse the submitted task", r.events)
				}
			} else {
				record := r.add(offlineHashA)
				if _, err := r.s.TryNext(ctx, record.ID); err != nil {
					t.Fatal(err)
				}
				r.poll(30 * time.Second)
				if len(r.events) != 3 || r.remote[offlineHashB].DirectoryID != "downloads" {
					t.Fatal("replacement lost the selected download directory", r.events)
				}
			}
		})
	}
}

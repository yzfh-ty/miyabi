package offline

import (
	"context"
	"testing"

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

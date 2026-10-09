package offline

import (
	"context"
	"errors"
	"testing"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent/offlinedownload"
	"github.com/ppxb/miyabi/internal/ent/task"
	"github.com/ppxb/miyabi/internal/pan"
)

func TestOfflineDuplicateOutsideLibrary(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      int
		isDirectory bool
		wantErr     bool
	}{
		{name: "completed video", status: 2},
		{name: "completed directory", status: 2, isDirectory: true},
		{name: "failed download", status: -1, isDirectory: true},
		{name: "queued download", status: 0, wantErr: true},
		{name: "active download", status: 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, client := offlineAddFixture(t)
			ctx := t.Context()
			source := *service.drive.Source()
			policy := domain.DirectoryPolicy{
				AccountID: source.AccountID, ParentID: source.Directory.ID,
				DownloadDirectory: domain.LibraryDirectory{ID: "downloads", Name: "Downloads"},
			}
			if err := database.SaveSetting(ctx, service.database, database.PanDirectorySettingsKey, policy); err != nil {
				t.Fatal(err)
			}
			adds, removes, outputChecks := 0, 0, 0
			client.addOffline = func(_ context.Context, _, uri, directoryID string) (string, error) {
				adds++
				if directoryID != "downloads" || uri != "magnet:?xt=urn:btih:"+offlineHashA {
					t.Fatalf("unexpected submission: directory=%s uri=%s", directoryID, uri)
				}
				if adds == 1 {
					return "", pan.ErrOfflineExists
				}
				if removes != 1 {
					t.Fatal("resubmitted before removing duplicate history")
				}
				return offlineHashA, nil
			}
			client.offlineTasks = func(context.Context, string, int) (pan.OfflinePage, error) {
				return pan.OfflinePage{PageCount: 1, Tasks: []pan.OfflineTask{{
					Hash: offlineHashA, Status: test.status, FileID: "old-output", DirectoryID: "outside",
				}}}, nil
			}
			client.info = func(_ context.Context, _, id string) (pan.FileInfo, error) {
				if id == "downloads" {
					return pan.FileInfo{
						File: pan.File{ID: id, ParentID: source.Directory.ID, Name: "Downloads", IsDirectory: true},
						Path: []pan.Directory{{ID: source.Directory.ID}},
					}, nil
				}
				if id != "old-output" {
					t.Fatalf("unexpected resource check: %s", id)
				}
				outputChecks++
				return pan.FileInfo{
					File: pan.File{ID: id, ParentID: "outside", Name: "ABP-001.mp4", IsDirectory: test.isDirectory},
					Path: []pan.Directory{{ID: "0"}, {ID: "outside"}},
				}, nil
			}
			client.list = func(context.Context, string, string, int, int) (pan.FilePage, error) {
				t.Fatal("must not traverse files outside the library")
				return pan.FilePage{}, nil
			}
			client.removeOffline = func(_ context.Context, _, hash string) error {
				if hash != offlineHashA {
					t.Fatalf("removed unrelated history: %s", hash)
				}
				removes++
				return nil
			}
			scansBefore := service.database.Task.Query().Where(task.TypeEQ("scan")).CountX(ctx)
			result, err := service.Add(ctx, "fixture-movie", offlineHashA)
			if test.wantErr {
				if err == nil || adds != 1 || removes != 0 || outputChecks != 0 {
					t.Fatalf("active task was replaced: adds=%d removes=%d checks=%d err=%v", adds, removes, outputChecks, err)
				}
				record := service.database.OfflineDownload.Query().OnlyX(ctx)
				if record.Recovery == nil || record.Recovery.Action != actionSubmit || record.Recovery.Failures != 1 ||
					record.Recovery.RetryAt.IsZero() || record.Error == nil || *record.Error == "" || record.ScanTaskID != 0 {
					t.Fatalf("conflicting submission lost its recovery checkpoint: %+v", record)
				}
				if count := service.database.Task.Query().Where(task.TypeEQ("scan")).CountX(ctx); count != scansBefore {
					t.Fatalf("conflicting output was queued for scanning: %d", count)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if adds != 2 || removes != 1 || outputChecks != 1 || result.Status != string(offlinedownload.StatusRunning) ||
				result.Phase != "downloading" || result.DirectoryID != source.Directory.ID || result.ScanTaskID != 0 {
				t.Fatalf("outside history blocked redownload: adds=%d removes=%d checks=%d result=%+v", adds, removes, outputChecks, result)
			}
			if count := service.database.OfflineDownload.Query().CountX(ctx); count != 1 {
				t.Fatalf("new download count = %d; want 1", count)
			}
			if count := service.database.Task.Query().Where(task.TypeEQ("scan")).CountX(ctx); count != 1 {
				t.Fatalf("outside output was queued for scanning: %d scans", count)
			}
		})
	}
}

func TestOfflineDuplicateRecoveryStopsOnErrors(t *testing.T) {
	for _, stage := range []string{"info", "removal", "resubmission"} {
		t.Run(stage, func(t *testing.T) {
			service, client := offlineAddFixture(t)
			failure := errors.New("fixture failure")
			adds, removes := 0, 0
			client.addOffline = func(context.Context, string, string, string) (string, error) {
				adds++
				if adds == 1 {
					return "", pan.ErrOfflineExists
				}
				return "", failure
			}
			client.offlineTasks = func(context.Context, string, int) (pan.OfflinePage, error) {
				return pan.OfflinePage{PageCount: 1, Tasks: []pan.OfflineTask{{
					Hash: offlineHashA, Status: 2, FileID: "old-output", DirectoryID: "outside",
				}}}, nil
			}
			client.info = func(context.Context, string, string) (pan.FileInfo, error) {
				if stage == "info" {
					return pan.FileInfo{}, failure
				}
				return pan.FileInfo{File: pan.File{ID: "old-output", Name: "video.mp4"}, Path: []pan.Directory{{ID: "outside"}}}, nil
			}
			client.removeOffline = func(context.Context, string, string) error {
				removes++
				if stage == "removal" {
					return failure
				}
				return nil
			}
			_, err := service.Add(t.Context(), "fixture-movie", offlineHashA)
			wantAdds, wantRemoves := 1, 1
			if stage == "info" {
				wantRemoves = 0
			} else if stage == "resubmission" {
				wantAdds = 2
			}
			if !errors.Is(err, failure) || adds != wantAdds || removes != wantRemoves {
				t.Fatalf("failed recovery: adds=%d removes=%d err=%v", adds, removes, err)
			}
			record := service.database.OfflineDownload.Query().OnlyX(t.Context())
			if record.Recovery == nil || record.Recovery.Action != actionSubmit || !record.Recovery.SubmissionStarted ||
				record.Recovery.Failures != 1 || record.Recovery.RetryAt.IsZero() || record.Error == nil || *record.Error == "" || record.ScanTaskID != 0 {
				t.Fatalf("unsuccessful submission lost its recovery checkpoint: %+v", record)
			}
		})
	}
}

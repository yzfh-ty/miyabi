package library

import (
	"context"
	"fmt"
	"testing"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent/task"
	"github.com/ppxb/miyabi/internal/pan"
	"github.com/ppxb/miyabi/internal/tasks"
)

type metadataSyncStub struct{ calls int }

func (s *metadataSyncStub) Sync(context.Context) error {
	s.calls++
	return nil
}

func TestScansRespectDownloadDirectoryAndSyncOtherMedia(t *testing.T) {
	for _, targeted := range []bool{false, true} {
		t.Run(fmt.Sprintf("targeted=%t", targeted), func(t *testing.T) {
			lib, queued, payload := libraryFixture(t)
			ctx := t.Context()
			policy := domain.DirectoryPolicy{
				AccountID: payload.Source.AccountID, ParentID: payload.Source.Directory.ID,
				DownloadDirectory: domain.LibraryDirectory{ID: "downloads", Name: "Downloads", Path: "Downloads"},
			}
			if err := database.SaveSetting(ctx, lib.database, database.PanDirectorySettingsKey, policy); err != nil {
				t.Fatal(err)
			}
			client := stubOf(t, lib.drive)
			client.list = func(_ context.Context, _, id string, _, _ int) (pan.FilePage, error) {
				var entries []pan.File
				switch id {
				case payload.Source.Directory.ID:
					entries = []pan.File{
						{ID: "downloads", Name: "Downloads", IsDirectory: true},
						{ID: "existing", Name: "Existing metadata", IsDirectory: true},
					}
				case "downloads":
					entries = []pan.File{{ID: "101", ParentID: "downloads", Name: "ABP-001.mp4", Size: 1 << 30}}
				default:
					t.Fatalf("media scanning entered a metadata-only directory: %s", id)
				}
				return pan.FilePage{Files: entries, Total: len(entries), Path: []pan.Directory{{ID: payload.Source.Directory.ID, Name: "Movies"}}}, nil
			}
			if targeted {
				payload.TargetID = "existing"
				client.info = func(_ context.Context, _, id string) (pan.FileInfo, error) {
					return pan.FileInfo{
						File: pan.File{ID: id, Name: "Existing metadata", ParentID: payload.Source.Directory.ID, IsDirectory: true},
						Path: []pan.Directory{{ID: payload.Source.Directory.ID, Name: "Movies"}},
					}, nil
				}
			}
			syncer := &metadataSyncStub{}
			lib.SetMetadataSyncer(syncer)
			encoded, err := tasks.EncodePayload(payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := lib.Scan(ctx, tasks.Job{ID: queued.ID, Payload: encoded}); err != nil {
				t.Fatal(err)
			}
			wantMovies := 1
			if targeted {
				wantMovies = 0
			}
			if lib.database.Movie.Query().CountX(ctx) != wantMovies ||
				lib.database.Task.Query().Where(task.TypeEQ(tasks.KindScrape.String())).CountX(ctx) != wantMovies {
				t.Fatal("the scanner did not limit scraping to the download directory")
			}
			if syncer.calls != 1 {
				t.Fatal("metadata-only directories were not synced after the scan")
			}
			completed, err := tasks.DecodePayload[domain.ScanPayload](lib.database.Task.GetX(ctx, queued.ID).Payload)
			if err != nil || completed.Scan.Stage != "done" || completed.Scan.MetadataOnly != targeted {
				t.Fatalf("scan result = %+v, %v", completed, err)
			}
		})
	}
}

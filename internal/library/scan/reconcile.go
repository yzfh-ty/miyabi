package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/ent/file"
	"github.com/ppxb/miyabi/internal/ent/movie"
	"github.com/ppxb/miyabi/internal/ent/predicate"
	"github.com/ppxb/miyabi/internal/ent/task"
	"github.com/ppxb/miyabi/internal/export"
	mediaimage "github.com/ppxb/miyabi/internal/image"
	"github.com/ppxb/miyabi/internal/library/scrape"
	"github.com/ppxb/miyabi/internal/tasks"
)

const reconcileBatchSize = 100

// ReconcileScan prepares cached exports before atomically committing index
// cleanup, metadata jobs, Emby notifications and scan completion.
func ReconcileScan(ctx context.Context, db *ent.Client, taskID int, payload *domain.ScanPayload, images *mediaimage.Cache, tasksSvc *tasks.Service, cfg export.Config, notifier scrape.MediaNotifier) error {
	run := scanRun{scanner: &Scanner{db: db, images: images, tasksSvc: tasksSvc, notifier: notifier, exportMgr: export.NewManager(cfg)}, taskID: taskID, payload: payload}
	return run.reconcile(ctx)
}

func (r *scanRun) staleFiles() predicate.File {
	stale := file.And(database.LibraryFiles(r.payload.Source), file.ScanIDNEQ(r.payload.ScanID))
	if r.payload.TargetID != "" {
		if r.payload.TargetFile {
			stale = file.And(stale, file.FileIDEQ(r.payload.TargetID))
		} else {
			prefix := strings.TrimSuffix(r.payload.TargetPath, "/") + "/"
			stale = file.And(stale, func(s *sql.Selector) {
				s.Where(sql.ExprP("substr("+s.C(file.FieldPath)+", 1, length(?)) = ?", prefix, prefix))
			})
		}
	}
	return stale
}

// Prepare only bounded pages of completed movies. Files that reconciliation
// will delete must not participate in snapshot comparison or regenerated STRMs.
func (r *scanRun) prepareReconcile(ctx context.Context, cfg export.Config) (map[int]time.Time, error) {
	if r.payload.Rebuild {
		return nil, nil
	}
	policy, err := database.LoadDirectoryPolicy(ctx, r.scanner.db, r.payload.Source)
	if err != nil {
		return nil, err
	}
	indexed := file.And(database.LibraryFiles(r.payload.Source), file.ScanIDEQ(r.payload.ScanID))
	retained := file.And(database.LibraryFiles(r.payload.Source), file.Not(r.staleFiles()))
	reusable := make(map[int]time.Time)
	for after := 0; ; {
		if err := tasks.Checkpoint(ctx, r.scanner.db); err != nil {
			return nil, err
		}
		query := r.scanner.db.Movie.Query().Where(movie.IDGT(after), movie.ScrapeStatusEQ(movie.ScrapeStatusDone), movie.HasFilesWith(indexed)).
			Order(movie.ByID()).Limit(reconcileBatchSize).
			WithFiles(func(q *ent.FileQuery) { q.Where(retained) })
		records, err := query.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("load completed scan movies: %w", err)
		}
		if len(records) == 0 {
			return reusable, nil
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			record.Edges.Files = slices.DeleteFunc(record.Edges.Files, func(entry *ent.File) bool {
				return !policy.ShouldScrape(r.payload.Source, entry.ParentID, entry.Path)
			})
			if len(record.Edges.Files) == 0 {
				continue
			}
			if !scrape.SnapshotMatches(record, r.payload.Source) {
				continue
			}
			cached, err := r.scanner.images.Exists(scrape.MovieArtwork(record))
			if err != nil {
				return nil, fmt.Errorf("check cached artwork: %w", err)
			}
			if !cached {
				continue
			}
			if cfg.EmbyDir != "" {
				if _, err := scrape.ExportLocalMovie(cfg.EmbyDir, cfg.PublicURL, cfg.STRMToken, record, r.scanner.images); err != nil {
					return nil, fmt.Errorf("export local movie %s: %w", record.Code, err)
				}
			}
			reusable[record.ID] = record.UpdatedAt
		}
		after = records[len(records)-1].ID
	}
}

// reconcileTx performs database work only; removed exports are cleaned after commit.
func (r *scanRun) reconcileTx(ctx context.Context, tx *ent.Tx, cfg export.Config, reusable map[int]time.Time) error {
	policy, err := database.LoadDirectoryPolicy(ctx, tx.Client(), r.payload.Source)
	if err != nil {
		return err
	}
	stale := r.staleFiles()
	staleMovies, err := tx.File.Query().Where(stale).QueryMovie().Select(movie.FieldID, movie.FieldCode).
		WithFiles(func(q *ent.FileQuery) {
			q.Where(stale).Select(file.FieldID, file.FieldMovieID, file.FieldParentID, file.FieldPath)
		}).All(ctx)
	if err != nil {
		return fmt.Errorf("find removed movie files: %w", err)
	}
	movies := make([]int, 0, len(staleMovies))
	preserveExports := make(map[string]bool)
	for _, record := range staleMovies {
		movies = append(movies, record.ID)
		for _, entry := range record.Edges.Files {
			if !policy.ShouldScrape(r.payload.Source, entry.ParentID, entry.Path) {
				preserveExports[record.Code] = true
			}
		}
	}
	r.payload.Scan.RemovedFiles, err = tx.File.Delete().Where(stale).Exec(ctx)
	if err != nil {
		return fmt.Errorf("remove missing file indexes: %w", err)
	}
	removed, err := removeUnreferencedMovies(ctx, tx, movies)
	if err != nil {
		return err
	}
	r.payload.Scan.RemovedMovies += len(removed)
	if cfg.EmbyDir != "" && len(removed) > 0 {
		tx.OnCommit(func(next ent.Committer) ent.Committer {
			return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
				if err := next.Commit(ctx, tx); err != nil {
					return err
				}
				var cleanupErrors []error
				for _, code := range removed {
					// Switching to metadata sync must not delete existing local files.
					if preserveExports[code] {
						continue
					}
					movieDir := scrape.EmbyMovieDir(cfg.EmbyDir, code)
					if err := os.RemoveAll(movieDir); err != nil {
						cleanupErrors = append(cleanupErrors, fmt.Errorf("remove exported movie %s after commit: %w", code, err))
						continue
					}
					_ = os.Remove(filepath.Dir(movieDir)) // Keep non-empty prefix directories.
					if r.scanner.notifier != nil {
						cleanupErrors = append(cleanupErrors, r.scanner.notifier.NotifyUpdated(ctx, movieDir))
					}
				}
				return errors.Join(cleanupErrors...)
			})
		})
	}
	indexed := file.And(database.LibraryFiles(r.payload.Source), file.ScanIDEQ(r.payload.ScanID))
	if r.payload.OfflineTaskID != 0 {
		files, err := tx.File.Query().Where(indexed).Select(file.FieldFileID).All(ctx)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(files))
		for _, entry := range files {
			ids = append(ids, entry.FileID)
		}
		if err := tx.OfflineDownload.UpdateOneID(r.payload.OfflineTaskID).SetFileIds(ids).Exec(ctx); err != nil {
			return err
		}
	}
	queued := false
	r.payload.ReusedTasks = nil
	for after := 0; ; {
		records, err := tx.Movie.Query().Where(movie.IDGT(after), movie.HasFilesWith(indexed)).
			Select(movie.FieldID, movie.FieldCode, movie.FieldJavdbID, movie.FieldManualCode, movie.FieldUpdatedAt).
			Order(movie.ByID()).Limit(reconcileBatchSize).
			WithFiles(func(q *ent.FileQuery) {
				q.Where(database.LibraryFiles(r.payload.Source)).Select(file.FieldMovieID, file.FieldParentID, file.FieldPath)
			}).All(ctx)
		if err != nil {
			return fmt.Errorf("find scanned metadata jobs: %w", err)
		}
		if len(records) == 0 {
			break
		}
		builders := make([]*ent.TaskCreate, 0, len(records))
		keys := make([]string, len(records))
		for i, record := range records {
			keys[i] = fmt.Sprintf("movie:%d", record.ID)
		}
		var active []struct {
			ID          int    `json:"id"`
			ResourceKey string `json:"resource_key"`
			ParentID    int    `json:"parent_id"`
		}
		if err := tx.Task.Query().Where(task.TypeEQ(string(tasks.KindScrape)), task.ResourceKeyIn(keys...),
			task.StatusIn(task.StatusQueued, task.StatusRunning), func(s *sql.Selector) {
				if r.payload.Rebuild {
					s.Where(sqljson.ValueEQ(task.FieldPayload, true, sqljson.Path("rebuild")))
				}
				// Publication checkpoints can precede the queue's final status update.
				// Such a task can no longer pick up files found by this scan.
				s.Where(sql.ExprP("coalesce(" + tasks.JSONExtract(s.C(task.FieldPayload), "completed") + ", 0) = 0"))
				s.Where(sql.And(sqljson.ValueEQ(task.FieldPayload, r.payload.Source.AccountID, sqljson.Path("source", "account_id")),
					sqljson.ValueEQ(task.FieldPayload, r.payload.Source.Directory.ID, sqljson.Path("source", "directory", "id"))))
				s.Select(s.C(task.FieldID), s.C(task.FieldResourceKey), sql.As(tasks.JSONExtract(s.C(task.FieldPayload), "scan_task_id"), "parent_id"))
			}).Order(task.ByID()).Select(task.FieldID).Scan(ctx, &active); err != nil {
			return err
		}
		byKey := make(map[string]int, len(active))
		for i, item := range active {
			if _, found := byKey[item.ResourceKey]; !found {
				byKey[item.ResourceKey] = i
			}
		}
		for _, record := range records {
			if !slices.ContainsFunc(record.Edges.Files, func(entry *ent.File) bool {
				return policy.ShouldScrape(r.payload.Source, entry.ParentID, entry.Path)
			}) {
				continue
			}
			if index, found := byKey[fmt.Sprintf("movie:%d", record.ID)]; found {
				item := active[index]
				if item.ParentID != r.taskID {
					r.payload.ReusedTasks = append(r.payload.ReusedTasks, item.ID)
				}
				continue
			}
			if checked, ok := reusable[record.ID]; ok && checked.Equal(record.UpdatedAt) {
				if cfg.EmbyDir != "" && r.scanner.notifier != nil {
					// Also notify on a retry that finds complete exports: a previous
					// attempt may have written them before its database commit failed.
					if err := r.scanner.notifier.NotifyUpdatedTx(ctx, tx, scrape.EmbyMovieDir(cfg.EmbyDir, record.Code)); err != nil {
						return err
					}
				}
				continue
			}
			input := scrape.MetadataPayload{
				Rebuild: r.payload.Rebuild,
				Source:  r.payload.Source, ScanTaskID: r.taskID, MovieID: record.ID,
				Code: record.Code, JavDBID: domain.ValueOrZero(record.JavdbID), ManualCode: record.ManualCode,
			}
			encoded, err := tasks.EncodePayload(input)
			if err != nil {
				return err
			}
			builders = append(builders, tx.Task.Create().SetType(tasks.KindScrape.String()).SetPayload(encoded).
				SetResourceKey(fmt.Sprintf("movie:%d", record.ID)))
		}
		if len(builders) > 0 {
			if err := tx.Task.CreateBulk(builders...).Exec(ctx); err != nil {
				return fmt.Errorf("enqueue movie metadata: %w", err)
			}
			queued = true
		}
		after = records[len(records)-1].ID
	}
	r.payload.Scan.Stage = "done"
	if err := SaveScanProgress(ctx, tx.Task, r.taskID, *r.payload); err != nil {
		return err
	}
	var change tasks.Change
	if r.payload.Scan.RemovedFiles > 0 || r.payload.Scan.RemovedMovies > 0 {
		change = tasks.ChangeLibrary
	}
	r.notifyAfterCommit(tx, change, queued)
	return nil
}

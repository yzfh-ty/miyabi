package sidecarsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/drive"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/export"
	"github.com/ppxb/miyabi/internal/pan"
)

const (
	configKey   = database.PanDirectorySettingsKey
	maxFileSize = 32 << 20
)

type Directory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Mode string `json:"mode,omitempty"`
}

type Config struct {
	Enabled            bool                    `json:"enabled"`
	AccountID          string                  `json:"account_id"`
	ParentID           string                  `json:"parent_id"`
	Destination        string                  `json:"destination"`
	DefaultDestination string                  `json:"default_destination"`
	DownloadDirectory  domain.LibraryDirectory `json:"download_directory"`
	ChildDirectories   []Directory             `json:"child_directories"`
	IntervalMinutes    int                     `json:"interval_minutes"`
	LastRunAt          *time.Time              `json:"last_run_at,omitempty"`
	LastResult         string                  `json:"last_result,omitempty"`
	FilesDownloaded    int                     `json:"files_downloaded"`
	FilesGenerated     int                     `json:"files_generated"`
	FilesSkipped       int                     `json:"files_skipped"`
	Errors             []string                `json:"errors,omitempty"`
}

type Update struct {
	Enabled           bool                    `json:"enabled"`
	Destination       string                  `json:"destination"`
	DownloadDirectory domain.LibraryDirectory `json:"download_directory"`
	ChildDirectories  []Directory             `json:"child_directories"`
	IntervalMinutes   int                     `json:"interval_minutes"`
}

type Service struct {
	db        *ent.Client
	drive     *drive.Drive
	exportMgr *export.Manager
	notifier  export.MediaNotifier
	log       *slog.Logger
	mu        sync.Mutex
}

func New(db *ent.Client, d *drive.Drive, exportMgr *export.Manager, notifier export.MediaNotifier, logger *slog.Logger) *Service {
	return &Service{db: db, drive: d, exportMgr: exportMgr, notifier: notifier, log: logger}
}

type syncResult struct {
	downloaded int
	generated  int
	skipped    int
	failures   []string
}

func (s *Service) Config(ctx context.Context) (Config, error) {
	config, _, err := database.LoadSetting[Config](ctx, s.db, configKey)
	if err != nil {
		return Config{}, err
	}
	if config.IntervalMinutes == 0 {
		config.IntervalMinutes = 30
	}
	if config.ChildDirectories == nil {
		config.ChildDirectories = []Directory{}
	}
	config.DefaultDestination, err = s.exportMgr.Config().RootDir()
	if err != nil {
		return Config{}, err
	}
	return config, nil
}

func (s *Service) destination(override string) (string, error) {
	embyRoot, err := s.exportMgr.Config().RootDir()
	if err != nil {
		return "", err
	}
	if override == "" {
		return embyRoot, nil
	}
	if !filepath.IsAbs(override) {
		return "", domain.E(domain.KindInvalid, "本地同步目录须为服务器上的绝对路径；留空自动使用 Emby 本地输出目录", nil)
	}
	root := filepath.Clean(override)
	if relative, err := filepath.Rel(filepath.Join(embyRoot, export.ManagedDirectory), root); err == nil && (relative == "." || filepath.IsLocal(relative)) {
		return "", domain.E(domain.KindInvalid, "miyabi 子目录专用于项目刮削，不能作为本地同步目录", nil)
	}
	return root, nil
}

func (s *Service) Update(ctx context.Context, input Update) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.IntervalMinutes < 1 || input.IntervalMinutes > 1440 {
		return Config{}, domain.E(domain.KindInvalid, "同步间隔须在 1 到 1440 分钟之间", nil)
	}
	destination := strings.TrimSpace(input.Destination)
	if input.Enabled {
		if _, err := s.destination(destination); err != nil {
			return Config{}, err
		}
	}

	sess, err := s.drive.Open(ctx)
	if err != nil {
		return Config{}, err
	}
	source := sess.Source()
	children, err := drive.DirectoryEntries(ctx, sess, source.Directory.ID)
	if err != nil {
		return Config{}, err
	}
	available := make(map[string]pan.File)
	for _, entry := range children {
		if entry.IsDirectory {
			available[entry.ID] = entry
		}
	}
	selected := make([]Directory, 0, len(input.ChildDirectories))
	var downloadDirectory domain.LibraryDirectory
	if id := input.DownloadDirectory.ID; id != "" {
		if id == source.Directory.ID {
			downloadDirectory = source.Directory
		} else if entry, ok := available[id]; ok {
			downloadDirectory = domain.LibraryDirectory{ID: entry.ID, Name: entry.Name, Path: entry.Name}
		} else {
			return Config{}, domain.E(domain.KindInvalid, "磁链下载目录必须是媒体根目录或其直接子目录", nil)
		}
	}
	seen := make(map[string]bool)
	for _, requested := range input.ChildDirectories {
		if requested.Mode != "" && requested.Mode != "scrape" && requested.Mode != "sync" {
			return Config{}, domain.E(domain.KindInvalid, "目录处理方式必须是刮削或同步元数据", nil)
		}
		entry, ok := available[requested.ID]
		if !ok || seen[requested.ID] {
			return Config{}, domain.E(domain.KindInvalid, "同步目录必须是当前挂载目录的直接子目录", nil)
		}
		seen[requested.ID] = true
		if !safeRelative(entry.Name) {
			return Config{}, domain.E(domain.KindInvalid, "115 子目录名称不能安全映射到本地路径", nil)
		}
		mode := requested.Mode
		if mode == "" {
			mode = "sync"
		}
		selected = append(selected, Directory{ID: entry.ID, Name: entry.Name, Path: entry.Name, Mode: mode})
	}
	slices.SortFunc(selected, func(a, b Directory) int { return strings.Compare(a.Name, b.Name) })

	config, err := s.Config(ctx)
	if err != nil {
		return Config{}, err
	}
	config.Enabled = input.Enabled
	config.AccountID = source.AccountID
	config.ParentID = source.Directory.ID
	config.Destination = destination
	if destination != "" {
		config.Destination = filepath.Clean(destination)
	}
	config.ChildDirectories = selected
	config.DownloadDirectory = downloadDirectory
	config.IntervalMinutes = input.IntervalMinutes
	config.LastRunAt = nil
	if err := sess.Commit(ctx, func(tx *ent.Tx) error {
		return database.SaveSetting(ctx, tx.Client(), configKey, config)
	}); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (s *Service) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.Config(ctx)
	if err != nil || !config.Enabled {
		return err
	}
	if config.LastRunAt != nil && time.Since(*config.LastRunAt) < time.Duration(config.IntervalMinutes)*time.Minute {
		return nil
	}
	return s.syncLocked(ctx, config)
}

func (s *Service) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.Config(ctx)
	if err != nil {
		return err
	}
	return s.syncLocked(ctx, config)
}

func (s *Service) syncLocked(ctx context.Context, config Config) error {
	if !config.Enabled {
		return nil
	}
	sess, err := s.drive.OpenSource(ctx, domain.LibrarySource{
		AccountID: config.AccountID,
		Directory: domain.LibraryDirectory{ID: config.ParentID},
	})
	if err != nil {
		return s.finish(ctx, config, syncResult{failures: []string{err.Error()}}, err)
	}
	root, err := s.destination(config.Destination)
	if err != nil {
		return s.finish(ctx, config, syncResult{failures: []string{err.Error()}}, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return s.finish(ctx, config, syncResult{failures: []string{err.Error()}}, err)
	}
	policy, err := database.LoadDirectoryPolicy(ctx, s.db, sess.Source())
	if err != nil {
		return s.finish(ctx, config, syncResult{failures: []string{err.Error()}}, err)
	}
	var result syncResult
	if err := s.walk(ctx, sess, root, config.ParentID, "", policy, &result); err != nil {
		result.failures = append(result.failures, err.Error())
	}
	if s.notifier != nil && (result.downloaded+result.generated > 0 || (config.LastResult != "" && config.LastResult != "success")) {
		if err := s.notifier.NotifyUpdated(ctx, root); err != nil {
			result.failures = append(result.failures, fmt.Sprintf("Emby 媒体库更新通知失败: %v", err))
		}
	}
	var runErr error
	if len(result.failures) > 0 {
		runErr = fmt.Errorf("sidecar sync completed with %d errors", len(result.failures))
	}
	if len(result.failures) > 20 {
		result.failures = append(result.failures[:20], fmt.Sprintf("还有 %d 条错误未显示", len(result.failures)-20))
	}
	return s.finish(ctx, config, result, runErr)
}

func (s *Service) walk(ctx context.Context, sess drive.Session, root, dirID, relative string, policy domain.DirectoryPolicy, result *syncResult) error {
	if relative != "" && !safeRelative(relative) {
		return fmt.Errorf("invalid relative path %q", relative)
	}
	entries, err := drive.DirectoryEntries(ctx, sess, dirID)
	if err != nil {
		return err
	}
	var videos []pan.File
	source := sess.Source()
	syncFiles := !policy.ShouldScrape(source, dirID, path.Join(source.Directory.Path, filepath.ToSlash(relative)))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		childRelative := filepath.Join(relative, entry.Name)
		if !safeRelative(childRelative) {
			result.failures = append(result.failures, fmt.Sprintf("%s: invalid relative path", childRelative))
			continue
		}
		if entry.IsDirectory {
			cloudPath := path.Join(source.Directory.Path, filepath.ToSlash(childRelative))
			if policy.ShouldScrape(source, entry.ID, cloudPath) {
				continue
			}
			if relative == "" && strings.EqualFold(entry.Name, export.ManagedDirectory) {
				embyRoot, err := s.exportMgr.Config().RootDir()
				if err != nil {
					return err
				}
				if root == embyRoot {
					result.failures = append(result.failures, "miyabi: 此目录保留用于项目刮削，请重命名 115 中的同名同步目录或设置独立的本地同步目录")
					continue
				}
			}
			info, err := drive.SourceInfo(ctx, sess, entry.ID)
			if err != nil || !info.IsDirectory || info.ParentID != dirID {
				if err == nil {
					err = errors.New("directory moved while syncing")
				}
				result.failures = append(result.failures, fmt.Sprintf("%s: %v", childRelative, err))
				continue
			}
			if err := s.walk(ctx, sess, root, entry.ID, childRelative, policy, result); err != nil {
				result.failures = append(result.failures, fmt.Sprintf("%s: %v", childRelative, err))
			}
			continue
		}
		if !syncFiles {
			continue
		}
		if domain.IsVideo(entry.Name) {
			videos = append(videos, entry)
			continue
		}
		if !sidecarFile(entry.Name) {
			continue
		}
		wasDownloaded, err := s.syncFile(ctx, sess, root, childRelative, entry)
		if err != nil {
			result.failures = append(result.failures, fmt.Sprintf("%s: %v", childRelative, err))
			continue
		}
		if wasDownloaded {
			result.downloaded++
		} else {
			result.skipped++
		}
	}
	// Publish STRMs after the directory's existing metadata has been synced.
	for _, video := range videos {
		if err := ctx.Err(); err != nil {
			return err
		}
		videoRelative := filepath.Join(relative, video.Name)
		generated, err := s.generateSTRM(root, videoRelative, video.ID)
		if err != nil {
			result.failures = append(result.failures, fmt.Sprintf("%s: %v", videoRelative, err))
			continue
		}
		if generated {
			result.generated++
		} else {
			result.skipped++
		}
	}
	return nil
}

func (s *Service) generateSTRM(root, videoRelative, fileID string) (bool, error) {
	relative := strings.TrimSuffix(videoRelative, filepath.Ext(videoRelative)) + ".strm"
	if !safeRelative(relative) {
		return false, fmt.Errorf("invalid relative path %q", relative)
	}
	target, err := ensureLocalTarget(root, relative)
	if err != nil {
		return false, err
	}
	var generated bool
	err = s.exportMgr.WithConfig(func(cfg export.Config) error {
		var err error
		generated, err = writeNewFile(target, export.STRMContent(cfg.PublicURL, fileID, cfg.STRMToken))
		return err
	})
	return generated, err
}

func (s *Service) syncFile(ctx context.Context, sess drive.Session, root, relative string, entry pan.File) (bool, error) {
	if !safeRelative(relative) {
		return false, fmt.Errorf("invalid relative path %q", relative)
	}
	target, err := ensureLocalTarget(root, relative)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(target); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	body, err := sess.Read(ctx, entry.PickCode, maxFileSize)
	if err != nil {
		return false, err
	}
	if entry.Size > 0 && int64(len(body)) != entry.Size {
		return false, fmt.Errorf("downloaded size %d does not match remote size %d", len(body), entry.Size)
	}
	if entry.SHA1 != "" && !strings.EqualFold(pan.SHA1(body), entry.SHA1) {
		return false, fmt.Errorf("downloaded SHA1 does not match 115")
	}
	return writeNewFile(target, body)
}

// writeNewFile publishes a complete file without replacing an existing target.
func writeNewFile(target string, body []byte) (bool, error) {
	if _, err := os.Lstat(target); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".sidecar-sync-*")
	if err != nil {
		return false, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tempName, target); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func ensureLocalTarget(root, relative string) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	parts := strings.FieldsFunc(relative, func(r rune) bool { return r == '/' || r == '\\' })
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path component %q is not a real directory", part)
		}
	}
	return filepath.Join(current, parts[len(parts)-1]), nil
}

func safeRelative(relative string) bool {
	if relative == "" || !filepath.IsLocal(relative) || filepath.Clean(relative) != relative {
		return false
	}
	portable := strings.ReplaceAll(relative, `\`, "/")
	for _, segment := range strings.Split(portable, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, ":") {
			return false
		}
	}
	return true
}

func sidecarFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".nfo", ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

func (s *Service) finish(ctx context.Context, config Config, result syncResult, runErr error) error {
	now := time.Now().UTC()
	config.LastRunAt = &now
	config.FilesDownloaded = result.downloaded
	config.FilesGenerated = result.generated
	config.FilesSkipped = result.skipped
	config.Errors = result.failures
	config.LastResult = "success"
	if runErr != nil {
		config.LastResult = runErr.Error()
	}
	if err := database.SaveSetting(ctx, s.db, configKey, config); err != nil {
		return errors.Join(runErr, err)
	}
	if runErr != nil && s.log != nil {
		s.log.WarnContext(ctx, "115 sidecar sync completed with errors", "downloaded", result.downloaded, "generated", result.generated, "skipped", result.skipped, "errors", result.failures)
	}
	return runErr
}

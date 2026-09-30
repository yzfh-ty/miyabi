package sidecarsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/drive"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/pan"
)

const (
	configKey   = "pan.sidecar_sync"
	maxFileSize = 32 << 20
)

type Directory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type Config struct {
	Enabled          bool        `json:"enabled"`
	AccountID        string      `json:"account_id"`
	ParentID         string      `json:"parent_id"`
	Destination      string      `json:"destination"`
	ChildDirectories []Directory `json:"child_directories"`
	IntervalMinutes  int         `json:"interval_minutes"`
	LastRunAt        *time.Time  `json:"last_run_at,omitempty"`
	LastResult       string      `json:"last_result,omitempty"`
	FilesDownloaded  int         `json:"files_downloaded"`
	FilesSkipped     int         `json:"files_skipped"`
	Errors           []string    `json:"errors,omitempty"`
}

type Update struct {
	Enabled          bool        `json:"enabled"`
	Destination      string      `json:"destination"`
	ChildDirectories []Directory `json:"child_directories"`
	IntervalMinutes  int         `json:"interval_minutes"`
}

type Service struct {
	db    *ent.Client
	drive *drive.Drive
	log   *slog.Logger
	mu    sync.Mutex
}

func New(db *ent.Client, d *drive.Drive, logger *slog.Logger) *Service {
	return &Service{db: db, drive: d, log: logger}
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
	return config, nil
}

func (s *Service) Update(ctx context.Context, input Update) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.IntervalMinutes < 1 || input.IntervalMinutes > 1440 {
		return Config{}, domain.E(domain.KindInvalid, "同步间隔须在 1 到 1440 分钟之间", nil)
	}
	destination := strings.TrimSpace(input.Destination)
	if input.Enabled && !filepath.IsAbs(destination) {
		return Config{}, domain.E(domain.KindInvalid, "启用同步时必须填写绝对本地目录", nil)
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
	seen := make(map[string]bool)
	for _, requested := range input.ChildDirectories {
		entry, ok := available[requested.ID]
		if !ok || seen[requested.ID] {
			return Config{}, domain.E(domain.KindInvalid, "同步目录必须是当前挂载目录的直接子目录", nil)
		}
		seen[requested.ID] = true
		if !safeRelative(entry.Name) {
			return Config{}, domain.E(domain.KindInvalid, "115 子目录名称不能安全映射到本地路径", nil)
		}
		selected = append(selected, Directory{ID: entry.ID, Name: entry.Name, Path: entry.Name})
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
	config.IntervalMinutes = input.IntervalMinutes
	if err := database.SaveSetting(ctx, s.db, configKey, config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (s *Service) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.Config(ctx)
	if err != nil || !config.Enabled || len(config.ChildDirectories) == 0 {
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
	if !config.Enabled || len(config.ChildDirectories) == 0 {
		return nil
	}
	sess, err := s.drive.OpenSource(ctx, domain.LibrarySource{
		AccountID: config.AccountID,
		Directory: domain.LibraryDirectory{ID: config.ParentID},
	})
	if err != nil {
		return s.finish(ctx, config, 0, 0, []string{err.Error()}, err)
	}
	if !filepath.IsAbs(config.Destination) {
		err := errors.New("sidecar sync destination must be absolute")
		return s.finish(ctx, config, 0, 0, []string{err.Error()}, err)
	}
	rootEntries, err := drive.DirectoryEntries(ctx, sess, config.ParentID)
	if err != nil {
		return s.finish(ctx, config, 0, 0, []string{err.Error()}, err)
	}
	selected := make(map[string]Directory, len(config.ChildDirectories))
	for _, child := range config.ChildDirectories {
		selected[child.ID] = child
	}
	downloaded, skipped := 0, 0
	var failures []string
	for _, entry := range rootEntries {
		child, ok := selected[entry.ID]
		if !ok {
			continue
		}
		if !entry.IsDirectory {
			failures = append(failures, fmt.Sprintf("选中的目录 %s 已不再是文件夹", child.Name))
			continue
		}
		info, err := drive.SourceInfo(ctx, sess, entry.ID)
		if err != nil || !info.IsDirectory || info.ParentID != config.ParentID {
			if err == nil {
				err = errors.New("directory is no longer a direct child of the mounted directory")
			}
			failures = append(failures, fmt.Sprintf("%s: %v", child.Name, err))
			continue
		}
		if err := s.walk(ctx, sess, config.Destination, child.ID, child.Path, &downloaded, &skipped, &failures); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", child.Name, err))
		}
	}
	for id, child := range selected {
		if !slices.ContainsFunc(rootEntries, func(entry pan.File) bool { return entry.ID == id && entry.IsDirectory }) {
			failures = append(failures, fmt.Sprintf("同步目录 %s 已从挂载目录移除", child.Name))
		}
	}
	var runErr error
	if len(failures) > 0 {
		runErr = fmt.Errorf("sidecar sync completed with %d errors", len(failures))
	}
	if len(failures) > 20 {
		failures = append(failures[:20], fmt.Sprintf("还有 %d 条错误未显示", len(failures)-20))
	}
	return s.finish(ctx, config, downloaded, skipped, failures, runErr)
}

func (s *Service) walk(ctx context.Context, sess drive.Session, root, dirID, relative string, downloaded, skipped *int, failures *[]string) error {
	if !safeRelative(relative) {
		return fmt.Errorf("invalid relative path %q", relative)
	}
	entries, err := drive.DirectoryEntries(ctx, sess, dirID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		childRelative := filepath.Join(relative, entry.Name)
		if !safeRelative(childRelative) {
			*failures = append(*failures, fmt.Sprintf("%s: invalid relative path", childRelative))
			continue
		}
		if entry.IsDirectory {
			if err := s.walk(ctx, sess, root, entry.ID, childRelative, downloaded, skipped, failures); err != nil {
				*failures = append(*failures, fmt.Sprintf("%s: %v", childRelative, err))
			}
			continue
		}
		if !sidecarFile(entry.Name) {
			continue
		}
		wasDownloaded, err := s.syncFile(ctx, sess, root, childRelative, entry)
		if err != nil {
			*failures = append(*failures, fmt.Sprintf("%s: %v", childRelative, err))
			continue
		}
		if wasDownloaded {
			*downloaded++
		} else {
			*skipped++
		}
	}
	return nil
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

func (s *Service) finish(ctx context.Context, config Config, downloaded, skipped int, failures []string, runErr error) error {
	now := time.Now().UTC()
	config.LastRunAt = &now
	config.FilesDownloaded = downloaded
	config.FilesSkipped = skipped
	config.Errors = failures
	config.LastResult = "success"
	if runErr != nil {
		config.LastResult = runErr.Error()
	}
	if err := database.SaveSetting(ctx, s.db, configKey, config); err != nil {
		return errors.Join(runErr, err)
	}
	if runErr != nil && s.log != nil {
		s.log.WarnContext(ctx, "115 sidecar sync completed with errors", "downloaded", downloaded, "skipped", skipped, "errors", failures)
	}
	return runErr
}

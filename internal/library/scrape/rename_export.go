package scrape

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/ent/movie"
	"github.com/ppxb/miyabi/internal/export"
	"github.com/ppxb/miyabi/internal/nfo"
)

// Remove files matching the previous movie's generated names after publishing
// the corrected identity. Unrelated files in the old directory remain untouched.
func (service *Service) removePreviousExport(ctx context.Context, root string, record *ent.Movie, code string) (string, error) {
	if root == "" || record.MetadataSnapshot == nil || record.MetadataSnapshot.Code == "" {
		return "", nil
	}
	oldCode := record.MetadataSnapshot.Code
	oldDir := export.EmbyMovieDir(root, oldCode)
	if strings.EqualFold(oldDir, export.EmbyMovieDir(root, code)) {
		return "", nil
	}
	claimed, err := service.db.Movie.Query().Where(movie.CodeEQ(oldCode), movie.IDNEQ(record.ID)).Exist(ctx)
	if err != nil || claimed {
		return "", err
	}
	entries, err := os.ReadDir(oldDir)
	if os.IsNotExist(err) {
		return oldDir, nil
	}
	if err != nil {
		return "", err
	}
	// Match the writer's exact naming rules, including positive disc numbers.
	generated := regexp.MustCompile(`^` + regexp.QuoteMeta(nfo.FileStem(oldCode)) + `(?:\.nfo|(?:-cd[1-9][0-9]*)?\.strm)$`)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (name != "poster.jpg" && name != "fanart.jpg" && !generated.MatchString(name)) {
			continue
		}
		if err := os.Remove(filepath.Join(oldDir, name)); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove previous export: %w", err)
		}
	}
	return oldDir, nil
}

package database

import (
	"context"

	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
)

const PanDirectorySettingsKey = "pan.sidecar_sync"

// Directory modes apply even when periodic metadata sync is paused.
func LoadDirectoryPolicy(ctx context.Context, db *ent.Client, source domain.LibrarySource) (domain.DirectoryPolicy, error) {
	policy, found, err := LoadSetting[domain.DirectoryPolicy](ctx, db, PanDirectorySettingsKey)
	if err != nil {
		return domain.DirectoryPolicy{}, err
	}
	if !found || policy.AccountID != source.AccountID || policy.ParentID != source.Directory.ID {
		return domain.DirectoryPolicy{}, nil
	}
	return policy, nil
}

package domain

import (
	"path"
	"strings"
)

type ProcessingDirectory struct {
	LibraryDirectory
	Mode string `json:"mode,omitempty"`
}

// DirectoryPolicy defaults to scraping only the selected download directory.
// Explicit subdirectory modes also apply to all of their descendants.
type DirectoryPolicy struct {
	AccountID         string                `json:"account_id"`
	ParentID          string                `json:"parent_id"`
	DownloadDirectory LibraryDirectory      `json:"download_directory"`
	ChildDirectories  []ProcessingDirectory `json:"child_directories"`
}

func (p DirectoryPolicy) ShouldScrape(source LibrarySource, id, filename string) bool {
	within := func(directory LibraryDirectory) bool {
		root := path.Join(source.Directory.Path, directory.Path)
		return id == directory.ID || filename == root || strings.HasPrefix(filename, root+"/")
	}
	for _, directory := range p.ChildDirectories {
		if within(directory.LibraryDirectory) {
			return directory.Mode == "scrape"
		}
	}
	if p.DownloadDirectory.ID == "" {
		return false
	}
	if p.DownloadDirectory.ID == source.Directory.ID {
		return true
	}
	return within(p.DownloadDirectory)
}

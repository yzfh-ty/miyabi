package atomicfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileRespectsUmaskForNewFiles(t *testing.T) {
	dir := t.TempDir()
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	path := filepath.Join(dir, "movie.strm")
	if err := WriteFile(path, []byte("new URL"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new file did not respect umask: %v, %v", info, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("replacement URL"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("existing permissions changed under stricter umask: %v, %v", info, err)
	}
}

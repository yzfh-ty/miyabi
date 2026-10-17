// Package atomicfile publishes complete files using same-directory replacements.
package atomicfile

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
)

// WriteFile writes to a temporary file and closes it before replacing name.
// A failed write or replacement leaves the existing file intact. Existing
// symlinks are followed and file permissions are preserved.
func WriteFile(name string, data []byte, perm os.FileMode) error {
	return writeFile(name, bytes.NewReader(data), perm)
}

func writeFile(name string, data io.Reader, perm os.FileMode) (err error) {
	info, err := os.Lstat(name)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		name, err = filepath.EvalSymlinks(name)
		if err != nil {
			return err
		}
		info, err = os.Stat(name)
	}
	existing := err == nil
	if existing {
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}

	temporary, err := os.OpenFile(filepath.Join(filepath.Dir(name), ".miyabi-"+rand.Text()+".tmp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		_ = temporary.Close()
		if err != nil {
			// Windows cannot remove a temporary file with read-only permissions.
			_ = os.Chmod(temporary.Name(), 0o600)
			_ = os.Remove(temporary.Name())
		}
	}()
	if _, err = io.Copy(temporary, data); err != nil {
		return err
	}
	if existing {
		// Preserve existing permissions even when the current umask is stricter.
		if err = temporary.Chmod(perm); err != nil {
			return err
		}
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	// os.Rename uses rename on Unix and MoveFileEx with REPLACE_EXISTING on
	// Windows. Never remove the destination first: replacement may fail.
	return os.Rename(temporary.Name(), name)
}

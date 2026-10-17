package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileCreatesAndReplacesCompleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	for _, body := range []string{"original metadata", "new", ""} {
		if err := WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != body {
			t.Fatalf("published content = %q, %v; want %q", got, err, body)
		}
		assertNoTemporaryFiles(t, dir)
	}
}

func TestWriteFailureKeepsOldFile(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new file", true: "existing file"}[existing], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "movie.nfo")
			const original = "complete original metadata"
			if existing {
				if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			checkDestination := func() {
				t.Helper()
				got, err := os.ReadFile(path)
				if existing {
					if err != nil || string(got) != original {
						t.Fatalf("old file changed during failed write: %q, %v", got, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("incomplete new file was published: %q, %v", got, err)
				}
			}
			failure := errors.New("interrupted write")
			data := io.MultiReader(strings.NewReader("partial new metadata"), readerFunc(func([]byte) (int, error) {
				checkDestination()
				return 0, failure
			}))
			if err := writeFile(path, data, 0o644); !errors.Is(err, failure) {
				t.Fatalf("write error = %v; want %v", err, failure)
			}
			checkDestination()
			assertNoTemporaryFiles(t, dir)
		})
	}
}

func TestReplacementFailureCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep")
	if err := os.WriteFile(marker, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	var renameErr *os.LinkError
	if err := WriteFile(path, []byte("new metadata"), 0o644); !errors.As(err, &renameErr) {
		t.Fatalf("expected replacement error, got %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "original" {
		t.Fatalf("failed replacement damaged destination: %q, %v", got, err)
	}
	assertNoTemporaryFiles(t, dir)
}

func TestWriteFileWithOpenReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.strm")
	if err := os.WriteFile(path, []byte("original URL"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	err = WriteFile(path, []byte("replacement URL"), 0o644)
	if runtime.GOOS == "windows" {
		// os.Open does not share delete access on Windows. A media reader can
		// prevent replacement, which must leave the original file untouched.
		if err == nil {
			t.Fatal("expected open reader to block replacement")
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "original URL" {
			t.Fatalf("failed replacement changed old file: %q, %v", got, err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(reader); err != nil || string(got) != "original URL" {
		t.Fatalf("open reader saw changed content: %q, %v", got, err)
	}
	assertNoTemporaryFiles(t, dir)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("replacement URL"), 0o644); err != nil {
		t.Fatalf("retry after reader closed: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "replacement URL" {
		t.Fatalf("retry content = %q, %v", got, err)
	}
	assertNoTemporaryFiles(t, dir)
}

func TestWriteFilePreservesPermissionsAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(path, []byte("original metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new metadata"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("replacement changed permissions: %v, %v", after, err)
	}
	link := filepath.Join(dir, "linked.nfo")
	if err := os.Symlink(filepath.Base(path), link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := WriteFile(link, []byte("updated through link"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(link); err != nil {
		t.Fatalf("symlink was replaced: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "updated through link" {
		t.Fatalf("symlink target = %q, %v", got, err)
	}
	assertNoTemporaryFiles(t, dir)
}

type readerFunc func([]byte) (int, error)

func (read readerFunc) Read(p []byte) (int, error) { return read(p) }

func assertNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary file was left behind: %s", entry.Name())
		}
	}
}

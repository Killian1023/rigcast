package syncfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWorkspaceArchiveIgnore(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		".gitignore":         "/root.txt\n*.tmp\n",
		".rigcastignore":     "# comment\nvenv/*\n!keep.tmp\n!/venv/*\nlocal-data/\n",
		".git/config":        "private",
		"venv/main.py":       "environment",
		"lib/node_modules/x": "package",
		"root.txt":           "excluded",
		"lib/root.txt":       "not root",
		"local-data/data":    "dataset",
		"drop.tmp":           "temporary",
		"keep.tmp":           "exception",
		"train.py":           "training",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	filter, err := WorkspaceFilter(root)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := Pack(&b, root, filter); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&b)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			files = append(files, h.Name)
		}
	}
	want := []string{".gitignore", ".rigcastignore", "keep.tmp", "lib/root.txt", "train.py"}
	if !slices.Equal(files, want) {
		t.Fatalf("archive files = %v, want %v", files, want)
	}
}

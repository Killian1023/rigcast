package syncfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsafeArchive(t *testing.T) {
	for _, header := range []tar.Header{
		{Name: "../escape", Mode: 0644},
		{Name: "/escape", Mode: 0644},
		{Name: `..\escape`, Mode: 0644},
		{Name: "deep/../../escape", Mode: 0644},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape"},
		{Name: "link", Typeflag: tar.TypeLink, Linkname: "../escape"},
	} {
		t.Run(header.Name, func(t *testing.T) {
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&header); err != nil {
				t.Fatal(err)
			}
			tw.Close()
			gz.Close()
			parent := t.TempDir()
			dst := filepath.Join(parent, "target")
			if err := os.Mkdir(dst, 0700); err != nil {
				t.Fatal(err)
			}
			if err := Extract(dst, &b, 1<<20); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(filepath.Join(parent, "escape")); !os.IsNotExist(err) {
				t.Fatalf("unexpected escape file: %v", err)
			}
		})
	}
}

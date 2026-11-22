package syncfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"sort"

	"rigcast/internal/models"
)

func Files(root *os.Root) ([]models.Artifact, error) {
	files := []models.Artifact{}
	err := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			files = append(files, models.Artifact{Path: name, Size: info.Size()})
		}
		return nil
	})
	return files, err
}

func Manifest(ctx context.Context, root *os.Root) (map[string]string, error) {
	result := map[string]string{}
	files, err := Files(root)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, _, err := OpenRegular(root, file.Path)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, contextReader{ctx, f})
		f.Close()
		if err != nil {
			return nil, err
		}
		result[file.Path] = hex.EncodeToString(h.Sum(nil))
	}
	return result, nil
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func Diff(local, remote map[string]string) models.Diff {
	diff := models.Diff{Upload: []string{}, Delete: []string{}}
	for name, hash := range local {
		if remote[name] != hash {
			diff.Upload = append(diff.Upload, name)
		}
	}
	for name := range remote {
		if _, ok := local[name]; !ok {
			diff.Delete = append(diff.Delete, name)
		}
	}
	sort.Strings(diff.Upload)
	sort.Strings(diff.Delete)
	return diff
}

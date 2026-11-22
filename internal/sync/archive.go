package syncfs

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func LocalPath(name string) (string, error) {
	if name == "." || strings.ContainsAny(name, "\\:\x00") {
		return "", fmt.Errorf("invalid relative path %q", name)
	}
	return filepath.Localize(name)
}

// Extract accepts only regular files and directories and never overwrites files.
func Extract(dst string, src io.Reader, maxExpanded int64) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	gz, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer gz.Close()
	expanded := &io.LimitedReader{R: gz, N: maxExpanded + 1}
	tr := tar.NewReader(expanded)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			// Consume the gzip trailer to verify the checksum and size.
			if _, err := io.Copy(io.Discard, expanded); err != nil {
				return err
			}
			if expanded.N == 0 {
				return errors.New("archive exceeds expanded size limit")
			}
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(strings.TrimSuffix(h.Name, "/"), "./")
		if (name == "." || name == "") && h.Typeflag == tar.TypeDir && (h.Name == "." || h.Name == "./") {
			continue
		}
		local, err := LocalPath(name)
		if err != nil {
			return fmt.Errorf("archive entry %q: %w", h.Name, err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(local, 0755)
		case tar.TypeReg:
			if h.Size > expanded.N-1 {
				return errors.New("archive exceeds expanded size limit")
			}
			if err = root.MkdirAll(filepath.Dir(local), 0755); err != nil {
				return err
			}
			var f *os.File
			f, err = root.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644|fs.FileMode(h.Mode)&0111)
			if err == nil {
				_, copyErr := io.CopyN(f, tr, h.Size)
				err = errors.Join(copyErr, f.Close())
				if err != nil {
					_ = root.Remove(local)
				}
			}
		default:
			return fmt.Errorf("unsupported archive entry %q", h.Name)
		}
		if err != nil {
			return err
		}
	}
}

type Filter func(path string, dir bool) bool

// Pack writes all regular files and directories selected by filter. Links and
// special files are not transferred.
func Pack(w io.Writer, root *os.Root, filter Filter) error {
	return writeArchive(w, func(tw *tar.Writer) error {
		return fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil || name == "." {
				return err
			}
			if filter != nil && !filter(name, d.IsDir()) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0755})
			}
			if d.Type().IsRegular() {
				return packFile(tw, root, name)
			}
			return nil
		})
	})
}

func PackFiles(w io.Writer, root *os.Root, names []string) error {
	return writeArchive(w, func(tw *tar.Writer) error {
		for _, name := range names {
			if err := packFile(tw, root, name); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeArchive(w io.Writer, fn func(*tar.Writer) error) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	if err := fn(tw); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func packFile(tw *tar.Writer, root *os.Root, name string) error {
	f, info, err := OpenRegular(root, name)
	if err != nil {
		return err
	}
	defer f.Close()
	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	h.Name = name
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	_, err = io.CopyN(tw, f, info.Size())
	return err
}

func OpenRegular(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	local, err := LocalPath(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := root.Lstat(local)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%q is not a regular file", name)
	}
	f, err := root.OpenFile(local, os.O_RDONLY|nonblock, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		f.Close()
		if err == nil {
			err = fmt.Errorf("%q changed while opening", name)
		}
		return nil, nil, err
	}
	return f, opened, nil
}

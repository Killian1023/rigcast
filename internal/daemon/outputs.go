package daemon

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"rigcast/internal/sanitize"
	syncfs "rigcast/internal/sync"
)

func tailOffset(f *os.File, lines int) (int64, error) {
	if lines == 0 {
		return 0, nil
	}
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	end := info.Size()
	var buf [32 << 10]byte
	for pos := end; pos > 0; {
		n := min(int64(len(buf)), pos)
		pos -= n
		if _, err := f.ReadAt(buf[:n], pos); err != nil {
			return 0, err
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' && pos+i != end-1 {
				lines--
				if lines == 0 {
					return pos + i + 1, nil
				}
			}
		}
	}
	return 0, nil
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	var tail int
	if v := query.Get("tail"); v != "" {
		var err error
		if tail, err = strconv.Atoi(v); err != nil || tail < 0 {
			problem(w, http.StatusBadRequest, "tail must be a nonnegative integer")
			return
		}
	}
	flags := map[string]bool{}
	for _, key := range []string{"sanitize", "follow"} {
		if v := query.Get(key); v != "" {
			var err error
			if flags[key], err = strconv.ParseBool(v); err != nil {
				problem(w, http.StatusBadRequest, key+" must be a boolean")
				return
			}
		}
	}
	f, err := os.Open(filepath.Join(s.jobsDir, j.ID, "logs.txt"))
	if err != nil {
		internalError(w, err)
		return
	}
	defer f.Close()
	offset, err := tailOffset(f, tail)
	if err == nil {
		_, err = f.Seek(offset, io.SeekStart)
	}
	if err != nil {
		internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flush := func() {}
	if flags["follow"] {
		flush = func() { _ = http.NewResponseController(w).Flush() }
		flush()
	}
	var pending []byte
	write := func(chunk []byte) error {
		if !flags["sanitize"] {
			_, err := w.Write(chunk)
			return err
		}
		for {
			i := bytes.IndexByte(chunk, '\n')
			if i < 0 {
				pending = append(pending, chunk...)
				return nil
			}
			pending = append(pending, chunk[:i+1]...)
			if _, err := io.WriteString(w, sanitize.Line(string(pending))); err != nil {
				return err
			}
			pending = pending[:0]
			chunk = chunk[i+1:]
		}
	}
	var buf [32 << 10]byte
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	final := j.Status.Terminal() || !flags["follow"]
	for r.Context().Err() == nil {
		n, err := f.Read(buf[:])
		if n != 0 {
			if write(buf[:n]) != nil {
				return
			}
			flush()
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			log.Printf("rigcast: read job %s logs: %v", j.ID, err)
			return
		}
		if final {
			if len(pending) != 0 {
				_, _ = io.WriteString(w, sanitize.Line(string(pending)))
			}
			return
		}
		current, err := s.db.Get(j.ID)
		if err != nil {
			return
		}
		if current.Status.Terminal() {
			// Drain bytes written between the previous read and the terminal state.
			final = true
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) outputRoot(w http.ResponseWriter, r *http.Request) (*os.Root, bool) {
	j, ok := s.findJob(w, r)
	if !ok {
		return nil, false
	}
	root, err := os.OpenRoot(filepath.Join(s.jobsDir, j.ID, "outputs"))
	if err != nil {
		internalError(w, err)
	}
	return root, err == nil
}

func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
	root, ok := s.outputRoot(w, r)
	if !ok {
		return
	}
	defer root.Close()
	files, err := syncfs.Files(root)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	root, ok := s.outputRoot(w, r)
	if !ok {
		return
	}
	defer root.Close()
	f, info, err := syncfs.OpenRegular(root, r.PathValue("path"))
	if err != nil {
		problem(w, http.StatusNotFound, "artifact not found")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *Server) artifactArchive(w http.ResponseWriter, r *http.Request) {
	root, ok := s.outputRoot(w, r)
	if !ok {
		return
	}
	defer root.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="artifacts.tar.gz"`)
	if err := syncfs.Pack(w, root, nil); err != nil {
		log.Printf("rigcast: stream artifact archive: %v", err)
	}
}

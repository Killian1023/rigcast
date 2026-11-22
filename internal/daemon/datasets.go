package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"

	"rigcast/internal/models"
	syncfs "rigcast/internal/sync"
)

const datasetLimit = 1 << 40

var datasetName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func datasetPath(w http.ResponseWriter, r *http.Request, dir string) (string, bool) {
	name := r.PathValue("name")
	if !datasetName.MatchString(name) {
		problem(w, http.StatusBadRequest, "invalid dataset name")
		return "", false
	}
	return filepath.Join(dir, name), true
}

func (s *Server) listDatasets(w http.ResponseWriter, _ *http.Request) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	dirs, err := os.ReadDir(s.datasetsDir)
	if err != nil {
		internalError(w, err)
		return
	}
	result := []models.Dataset{}
	for _, dir := range dirs {
		if !dir.IsDir() || !datasetName.MatchString(dir.Name()) {
			continue
		}
		root, err := os.OpenRoot(filepath.Join(s.datasetsDir, dir.Name()))
		if err != nil {
			internalError(w, err)
			return
		}
		files, err := syncfs.Files(root)
		root.Close()
		if err != nil {
			internalError(w, err)
			return
		}
		entry := models.Dataset{Name: dir.Name(), FileCount: len(files)}
		for _, file := range files {
			entry.Size += file.Size
		}
		result = append(result, entry)
	}
	writeJSON(w, http.StatusOK, result)
}

func openDataset(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("dataset path is not a directory")
	}
	return os.OpenRoot(dir)
}

func (s *Server) diffDataset(w http.ResponseWriter, r *http.Request) {
	dir, ok := datasetPath(w, r, s.datasetsDir)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	var manifest map[string]string
	dec := json.NewDecoder(r.Body)
	if dec.Decode(&manifest) != nil || manifest == nil || dec.Decode(new(any)) != io.EOF {
		problem(w, http.StatusBadRequest, "manifest must be a JSON object")
		return
	}
	for name, hash := range manifest {
		if _, err := syncfs.LocalPath(name); err != nil {
			problem(w, http.StatusBadRequest, err.Error())
			return
		}
		raw, err := hex.DecodeString(hash)
		if err != nil || len(raw) != sha256.Size {
			problem(w, http.StatusBadRequest, "invalid SHA-256 for "+name)
			return
		}
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, exists := manifest[ancestor]; exists {
				problem(w, http.StatusBadRequest, "manifest contains conflicting paths")
				return
			}
		}
		manifest[name] = hex.EncodeToString(raw)
	}
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	root, err := openDataset(dir)
	if errors.Is(err, fs.ErrNotExist) {
		writeJSON(w, http.StatusOK, syncfs.Diff(manifest, nil))
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	defer root.Close()
	remote, err := syncfs.Manifest(r.Context(), root)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, syncfs.Diff(manifest, remote))
}

func (s *Server) syncDataset(w http.ResponseWriter, r *http.Request) {
	dir, ok := datasetPath(w, r, s.datasetsDir)
	if !ok || !parseMultipart(w, r, datasetLimit) {
		return
	}
	defer r.MultipartForm.RemoveAll()
	var remove []string
	fields := r.MultipartForm.Value["delete"]
	if len(fields) != 1 || json.Unmarshal([]byte(fields[0]), &remove) != nil {
		problem(w, http.StatusBadRequest, "delete must be a JSON array")
		return
	}
	for i, name := range remove {
		local, err := syncfs.LocalPath(name)
		if err != nil {
			problem(w, http.StatusBadRequest, err.Error())
			return
		}
		remove[i] = local
	}
	f, _, err := r.FormFile("archive")
	if err != nil {
		problem(w, http.StatusBadRequest, "archive is required")
		return
	}
	defer f.Close()
	stage, err := os.MkdirTemp(s.datasetsDir, ".upload-")
	if err != nil {
		internalError(w, err)
		return
	}
	defer os.RemoveAll(stage)
	if err := syncfs.Extract(stage, f, datasetLimit); err != nil {
		problem(w, http.StatusBadRequest, fmt.Sprintf("invalid dataset archive: %v", err))
		return
	}
	stageRoot, err := os.OpenRoot(stage)
	if err != nil {
		internalError(w, err)
		return
	}
	defer stageRoot.Close()
	files, err := syncfs.Files(stageRoot)
	if err != nil {
		internalError(w, err)
		return
	}
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		internalError(w, err)
		return
	}
	root, err := openDataset(dir)
	if err != nil {
		internalError(w, err)
		return
	}
	defer root.Close()
	for _, name := range remove {
		info, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			problem(w, http.StatusBadRequest, "delete path is not a regular file: "+name)
			return
		}
	}
	for _, name := range remove {
		if err := root.Remove(name); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			internalError(w, err)
			return
		}
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			if root.Remove(parent) != nil {
				break
			}
		}
	}
	// Keep the dataset directory inode stable for existing Docker bind mounts.
	// Both paths are in the daemon's private dataset directory on one filesystem.
	for _, file := range files {
		local, err := syncfs.LocalPath(file.Path)
		if err == nil {
			err = root.MkdirAll(filepath.Dir(local), 0755)
		}
		if err == nil {
			err = os.Rename(filepath.Join(stage, local), filepath.Join(dir, local))
		}
		if err != nil {
			internalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"uploaded": len(files), "deleted": len(remove)})
}

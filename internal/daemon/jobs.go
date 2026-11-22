package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/docker/go-units"

	"rigcast/internal/docker"
	"rigcast/internal/models"
	syncfs "rigcast/internal/sync"
)

const workspaceLimit = 2 << 30

var gpuIDs = regexp.MustCompile(`^[0-9]+(,[0-9]+)*$`)

func validateSpec(spec *models.Submission) (time.Duration, int64, error) {
	if strings.TrimSpace(spec.Project) == "" || strings.TrimSpace(spec.Command) == "" || strings.TrimSpace(spec.Image) == "" {
		return 0, 0, errors.New("project, command, and image are required")
	}
	if spec.GPUs == "" {
		spec.GPUs = "all"
	}
	if spec.GPUs != "all" && spec.GPUs != "none" && !gpuIDs.MatchString(spec.GPUs) {
		return 0, 0, errors.New("gpus must be all, none, or comma-separated device IDs")
	}
	if spec.Workdir == "" {
		spec.Workdir = "/workspace"
	}
	spec.Workdir = path.Clean(spec.Workdir)
	if spec.Workdir != "/workspace" && !strings.HasPrefix(spec.Workdir, "/workspace/") {
		return 0, 0, errors.New("workdir must be /workspace or a subdirectory")
	}
	if spec.Datasets == nil {
		spec.Datasets = []string{}
	}
	seen := map[string]bool{}
	for _, name := range spec.Datasets {
		if !datasetName.MatchString(name) || seen[name] {
			return 0, 0, fmt.Errorf("invalid or duplicate dataset %q", name)
		}
		seen[name] = true
	}
	for name, value := range spec.Env {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			return 0, 0, fmt.Errorf("invalid environment variable %q", name)
		}
	}
	var timeout time.Duration
	if spec.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(spec.Timeout)
		if err != nil || timeout <= 0 {
			return 0, 0, errors.New("timeout must be a positive Go duration")
		}
	}
	var memory int64
	if spec.MemoryLimit != "" {
		var err error
		memory, err = units.RAMInBytes(spec.MemoryLimit)
		if err != nil || memory <= 0 {
			return 0, 0, errors.New("memory_limit must be a positive size, such as 32g")
		}
	}
	return timeout, memory, nil
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	if !parseMultipart(w, r, workspaceLimit) {
		return
	}
	defer r.MultipartForm.RemoveAll()
	metadata := r.MultipartForm.Value["metadata"]
	var spec models.Submission
	if len(metadata) != 1 || json.Unmarshal([]byte(metadata[0]), &spec) != nil {
		problem(w, http.StatusBadRequest, "metadata must be a JSON object")
		return
	}
	timeout, memory, err := validateSpec(&spec)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, name := range spec.Datasets {
		info, err := os.Lstat(filepath.Join(s.datasetsDir, name))
		if err != nil || !info.IsDir() {
			problem(w, http.StatusBadRequest, fmt.Sprintf("dataset %q does not exist", name))
			return
		}
	}
	archive, _, err := r.FormFile("workspace")
	if err != nil {
		problem(w, http.StatusBadRequest, "workspace archive is required")
		return
	}
	defer archive.Close()
	var key [12]byte
	_, err = rand.Read(key[:])
	if err != nil {
		internalError(w, err)
		return
	}
	id := hex.EncodeToString(key[:])
	dir := filepath.Join(s.jobsDir, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		internalError(w, err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, mount := range []string{"workspace", "outputs"} {
		dir := filepath.Join(dir, mount)
		if err := os.Mkdir(dir, 0755); err != nil {
			internalError(w, err)
			return
		}
		// Bind-mount roots must be writable even for images with a non-root USER.
		if err := os.Chmod(dir, 0777); err != nil {
			internalError(w, err)
			return
		}
	}
	if err := syncfs.Extract(filepath.Join(dir, "workspace"), archive, workspaceLimit); err != nil {
		problem(w, http.StatusBadRequest, fmt.Sprintf("invalid workspace archive: %v", err))
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "logs.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		internalError(w, err)
		return
	}
	f.Close()
	job := models.Job{
		ID: id, Project: spec.Project, Command: spec.Command, Image: spec.Image,
		Datasets: spec.Datasets, GPUs: spec.GPUs, Status: models.Queued, CreatedAt: time.Now().UTC(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		problem(w, http.StatusServiceUnavailable, "daemon shutting down")
		return
	}
	if err := s.db.Create(job); err != nil {
		internalError(w, err)
		return
	}
	committed = true
	s.launch(id, func(ctx context.Context) (int, error) {
		var cancel context.CancelFunc
		if timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		name := "rigcast-" + id
		if err := s.runner.EnsureImage(ctx, spec.Image, func() error { return s.db.Pulling(id) }); err != nil {
			return 0, jobContextError(ctx, err)
		}
		s.dataMu.RLock()
		container, err := s.runner.Start(ctx, name, dir, s.datasetsDir, spec, memory)
		s.dataMu.RUnlock()
		if container == "" {
			container = name
		}
		defer s.cleanupOnExit(ctx, container)
		if err != nil {
			return 0, jobContextError(ctx, err)
		}
		if err := s.db.Running(id, container); err != nil {
			return 0, err
		}
		return s.monitor(ctx, id, container)
	})
	writeJSON(w, http.StatusAccepted, job)
}

func jobContextError(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

// launch is called with s.mu held, before the queued job becomes cancellable.
func (s *Server) launch(id string, run func(context.Context) (int, error)) {
	ctx, cancel := context.WithCancelCause(context.Background())
	w := &worker{cancel: cancel, done: make(chan struct{})}
	s.workers[id] = w
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		code, err := run(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		defer cancel(nil)
		defer close(w.done)
		defer delete(s.workers, id)
		if errors.Is(context.Cause(ctx), errShutdown) {
			return // Leave active jobs and containers available for recovery.
		}
		status := models.Succeeded
		var exit *int
		var message string
		switch {
		case errors.Is(context.Cause(ctx), errCancelled):
			status = models.Cancelled
		case errors.Is(err, context.DeadlineExceeded):
			status, message = models.Failed, "timeout exceeded"
		case err != nil:
			status, message = models.Failed, err.Error()
		default:
			exit = &code
			if code != 0 {
				status = models.Failed
			}
		}
		if err := s.db.Finish(id, status, exit, message); err != nil {
			log.Printf("rigcast: persist job %s result: %v", id, err)
		}
	}()
}

func (s *Server) monitor(ctx context.Context, id, container string) (int, error) {
	f, err := os.OpenFile(filepath.Join(s.jobsDir, id, "logs.txt"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	logCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.runner.Logs(logCtx, container, f) }()
	code, err := s.runner.Wait(ctx, container)
	if err != nil {
		cancel()
	}
	if logErr := <-done; err == nil {
		err = logErr
	}
	return code, jobContextError(ctx, err)
}

func (s *Server) removeContainer(id string) {
	ctx, cancel := boundedContext(context.Background())
	defer cancel()
	if err := s.runner.Remove(ctx, id); err != nil {
		log.Printf("rigcast: remove container %s: %v", id, err)
	}
}

func (s *Server) cleanupOnExit(ctx context.Context, id string) {
	if !errors.Is(context.Cause(ctx), errShutdown) {
		s.removeContainer(id)
	}
}

func (s *Server) Recover(ctx context.Context) error {
	active, err := s.db.Active()
	if err != nil {
		return err
	}
	for _, j := range active {
		id := j.ContainerID
		if id == "" {
			id = "rigcast-" + j.ID
		}
		probe, cancel := boundedContext(ctx)
		state, err := s.runner.Inspect(probe, id)
		cancel()
		if err != nil {
			return fmt.Errorf("recover job %s: %w", j.ID, err)
		}
		if !state.Running {
			if state.ID != "" {
				s.removeContainer(state.ID)
			}
			if err := s.db.Finish(j.ID, models.Failed, nil, "daemon restarted"); err != nil {
				return err
			}
			continue
		}
		if err := s.db.Running(j.ID, state.ID); err != nil {
			return err
		}
		// Docker replays its available log history when a new follower attaches.
		if err := os.Truncate(filepath.Join(s.jobsDir, j.ID, "logs.txt"), 0); err != nil {
			return fmt.Errorf("reset logs for recovered job %s: %w", j.ID, err)
		}
		s.mu.Lock()
		s.launch(j.ID, func(ctx context.Context) (int, error) {
			ctx, cancel := recoveryContext(ctx, state)
			defer cancel()
			defer s.cleanupOnExit(ctx, state.ID)
			return s.monitor(ctx, j.ID, state.ID)
		})
		s.mu.Unlock()
	}
	return nil
}

func recoveryContext(ctx context.Context, state docker.State) (context.Context, context.CancelFunc) {
	if state.Deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, state.Deadline)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	if j, ok := s.findJob(w, r); ok {
		writeJSON(w, http.StatusOK, j)
	}
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if val := r.URL.Query().Get("limit"); val != "" {
		var err error
		if limit, err = strconv.Atoi(val); err != nil || limit < 1 || limit > 1000 {
			problem(w, http.StatusBadRequest, "limit must be between 1 and 1000")
			return
		}
	}
	status := models.Status(r.URL.Query().Get("status"))
	if status != "" && status != models.Queued && status != models.Pulling && status != models.Running && !status.Terminal() {
		problem(w, http.StatusBadRequest, "invalid job status")
		return
	}
	jobs, err := s.db.List(status, limit)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	worker := s.workers[j.ID]
	if worker != nil {
		worker.cancel(errCancelled)
	}
	s.mu.Unlock()
	if worker != nil {
		select {
		case <-r.Context().Done():
			return
		case <-worker.done:
		}
	}
	s.getJob(w, r)
}

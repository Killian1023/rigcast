package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rigcast/internal/docker"
	"rigcast/internal/gpu"
	"rigcast/internal/models"
	"rigcast/internal/store"
)

type Runner interface {
	EnsureImage(context.Context, string, func() error) error
	Start(context.Context, string, string, string, models.Submission, int64) (string, error)
	Inspect(context.Context, string) (docker.State, error)
	Logs(context.Context, string, io.Writer) error
	Wait(context.Context, string) (int, error)
	Remove(context.Context, string) error
}

type Server struct {
	db          *store.Store
	runner      Runner
	jobsDir     string
	datasetsDir string
	token       [32]byte
	version     string
	gpu         func(context.Context) ([]models.GPU, error)
	dataMu      sync.RWMutex
	mu          sync.Mutex
	workers     map[string]*worker
	closed      bool
	wg          sync.WaitGroup
}

var (
	errCancelled = errors.New("job cancelled")
	errShutdown  = errors.New("daemon shutting down")
)

type worker struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func New(dir, token, version string, runner Runner) (*Server, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("daemon token is required")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		runner: runner, token: sha256.Sum256([]byte(token)), version: version, gpu: gpu.Status,
		jobsDir: filepath.Join(dir, "jobs"), datasetsDir: filepath.Join(dir, "datasets"),
		workers: map[string]*worker{},
	}
	for _, d := range []string{s.jobsDir, s.datasetsDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return nil, err
		}
	}
	s.db, err = store.Open(filepath.Join(dir, "rigcast.db"))
	return s, err
}

func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for _, w := range s.workers {
		w.cancel(errShutdown)
	}
	s.mu.Unlock()
	s.wg.Wait()
	return s.db.Close()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, models.Health{Status: "ok", Version: s.version})
	})
	mux.HandleFunc("GET /api/v1/gpu", s.gpuStatus)
	mux.HandleFunc("POST /api/v1/jobs", s.submit)
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.cancelJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/logs", s.logs)
	mux.HandleFunc("GET /api/v1/jobs/{id}/artifacts", s.artifacts)
	mux.HandleFunc("GET /api/v1/jobs/{id}/artifacts/{path...}", s.artifact)
	mux.HandleFunc("GET /api/v1/jobs/{id}/artifacts.tar.gz", s.artifactArchive)
	mux.HandleFunc("GET /api/v1/datasets", s.listDatasets)
	mux.HandleFunc("POST /api/v1/datasets/{name}/diff", s.diffDataset)
	mux.HandleFunc("POST /api/v1/datasets/{name}/sync", s.syncDataset)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			sum := sha256.Sum256([]byte(token))
			if !ok || subtle.ConstantTimeCompare(sum[:], s.token[:]) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				problem(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) gpuStatus(w http.ResponseWriter, r *http.Request) {
	gpus, err := s.gpu(r.Context())
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	active, err := s.db.ActiveCount()
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, models.GPUStatus{GPUs: gpus, ActiveJobs: active})
}

func (s *Server) findJob(w http.ResponseWriter, r *http.Request) (models.Job, bool) {
	j, err := s.db.Get(r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, http.StatusNotFound, "job not found")
	} else if err != nil {
		internalError(w, err)
	}
	return j, err == nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func problem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("rigcast: %v", err)
	problem(w, http.StatusInternalServerError, "internal daemon error")
}

func parseMultipart(w http.ResponseWriter, r *http.Request, max int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			problem(w, http.StatusRequestEntityTooLarge, "upload exceeds size limit")
		} else {
			problem(w, http.StatusBadRequest, fmt.Sprintf("invalid multipart upload: %v", err))
		}
		return false
	}
	return true
}

func boundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 15*time.Second)
}

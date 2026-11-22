package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rigcast/internal/docker"
	"rigcast/internal/models"
	syncfs "rigcast/internal/sync"
)

type fakeRunner struct {
	exit    chan struct{}
	removed chan string
	state   docker.State
}

func (*fakeRunner) EnsureImage(_ context.Context, _ string, pulling func() error) error {
	return pulling()
}

func (*fakeRunner) Start(_ context.Context, id, dir, _ string, _ models.Submission, _ int64) (string, error) {
	body, err := os.ReadFile(filepath.Join(dir, "workspace", "input"))
	if err != nil {
		return id, err
	}
	return id, os.WriteFile(filepath.Join(dir, "outputs", "result"), body, 0600)
}

func (r *fakeRunner) Inspect(context.Context, string) (docker.State, error) {
	return r.state, nil
}

func (*fakeRunner) Logs(_ context.Context, _ string, dst io.Writer) error {
	_, err := io.WriteString(dst, "first\n5%\r\x1b[32m100%\x1b[0m\r\nlast")
	return err
}

func (r *fakeRunner) Wait(ctx context.Context, _ string) (int, error) {
	select {
	case <-r.exit:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (r *fakeRunner) Remove(_ context.Context, id string) error {
	r.removed <- id
	return nil
}

func testServer(t *testing.T, runner Runner) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(t.TempDir(), "test-token", "test", runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	server := httptest.NewServer(s.Handler())
	t.Cleanup(server.Close)
	return s, server
}

func request(t *testing.T, server *httptest.Server, method, path string, body io.Reader, contentType string, status int) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, server.URL+"/api/v1"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/health" && status != http.StatusUnauthorized {
		req.Header.Set("Authorization", "Bearer test-token")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != status {
		t.Fatalf("%s %s: got %d %s (%v), want %d", method, path, resp.StatusCode, data, err, status)
	}
	return data
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func submission(t *testing.T, server *httptest.Server, spec models.Submission) models.Job {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input"), []byte("test data"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	metadata, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("metadata", string(metadata)); err != nil {
		t.Fatal(err)
	}
	part, err := mw.CreateFormFile("workspace", "workspace.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if err := syncfs.Pack(part, root, nil); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	b := request(t, server, http.MethodPost, "/jobs", &body, mw.FormDataContentType(), http.StatusAccepted)
	j := decode[models.Job](t, b)
	if j.Status != models.Queued || j.ID == "" {
		t.Fatalf("unexpected response: %s", b)
	}
	return j
}

func TestJobHTTP(t *testing.T) {
	runner := &fakeRunner{exit: make(chan struct{}), removed: make(chan string, 1)}
	s, server := testServer(t, runner)
	s.gpu = func(context.Context) ([]models.GPU, error) {
		return []models.GPU{{Name: "test GPU"}}, nil
	}
	request(t, server, "GET", "/health", nil, "", http.StatusOK)
	request(t, server, "GET", "/jobs", nil, "", http.StatusUnauthorized)
	spec := models.Submission{Project: "test", Command: "train", Spec: models.Spec{Image: "alpine", GPUs: "none"}}
	j := submission(t, server, spec)
	base := "/jobs/" + j.ID
	status := decode[models.GPUStatus](t, request(t, server, "GET", "/gpu", nil, "", http.StatusOK))
	if status.ActiveJobs != 1 || len(status.GPUs) != 1 {
		t.Fatalf("GPU status: %+v", status)
	}
	close(runner.exit)
	logs := request(t, server, "GET", base+"/logs?tail=2&sanitize=true&follow=true", nil, "", http.StatusOK)
	// If tailing attaches before the job writes any bytes, all future bytes follow.
	if string(logs) != "100%\nlast" && string(logs) != "first\n100%\nlast" {
		t.Fatalf("logs = %q", logs)
	}
	current := decode[models.Job](t, request(t, server, "GET", base, nil, "", http.StatusOK))
	if current.Status != models.Succeeded || current.ExitCode == nil || *current.ExitCode != 0 {
		t.Fatalf("job = %+v", current)
	}
	if id := <-runner.removed; id != current.ContainerID {
		t.Fatalf("removed %q instead of %q", id, current.ContainerID)
	}
	data := request(t, server, "GET", base+"/artifacts/result", nil, "", http.StatusOK)
	if string(data) != "test data" {
		t.Fatalf("artifact = %q", data)
	}
	if err := os.Symlink(filepath.Join(s.jobsDir, j.ID, "logs.txt"), filepath.Join(s.jobsDir, j.ID, "outputs", "outside")); err == nil {
		request(t, server, "GET", base+"/artifacts/outside", nil, "", http.StatusNotFound)
	}
	entries := decode[[]models.Artifact](t, request(t, server, "GET", base+"/artifacts", nil, "", http.StatusOK))
	if len(entries) != 1 || entries[0].Path != "result" {
		t.Fatalf("artifacts = %+v", entries)
	}
	archive := request(t, server, "GET", base+"/artifacts.tar.gz", nil, "", http.StatusOK)
	if err := syncfs.Extract(t.TempDir(), bytes.NewReader(archive), 1<<20); err != nil {
		t.Fatal(err)
	}
}

func TestCancelAndRecover(t *testing.T) {
	runner := &fakeRunner{exit: make(chan struct{}), removed: make(chan string, 1)}
	s, server := testServer(t, runner)
	spec := models.Submission{Project: "test", Command: "train", Spec: models.Spec{Image: "alpine"}}
	j := submission(t, server, spec)
	base := "/jobs/" + j.ID
	got := decode[models.Job](t, request(t, server, "DELETE", base, nil, "", http.StatusOK))
	if got.Status != models.Cancelled {
		t.Fatalf("cancelled job = %+v", got)
	}

	queued := models.Job{ID: "abandoned", Status: models.Queued, CreatedAt: time.Now().UTC()}
	if err := s.db.Create(queued); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := s.db.Get(queued.ID)
	if err != nil || got.Status != models.Failed || got.Error != "daemon restarted" {
		t.Fatalf("recovered job = %+v, %v", got, err)
	}
}

func TestRestartReattachesRunningJob(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeRunner{exit: make(chan struct{}), removed: make(chan string, 1)}
	original, err := New(dir, "test-token", "test", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if original != nil {
			_ = original.Close()
		}
	}()
	server := httptest.NewServer(original.Handler())
	defer server.Close()
	j := submission(t, server, models.Submission{
		Project: "test", Command: "train", Spec: models.Spec{Image: "alpine"},
	})
	deadline := time.Now().Add(5 * time.Second)
	for j.Status != models.Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		j, err = original.db.Get(j.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if j.Status != models.Running {
		t.Fatalf("job status before shutdown: %s", j.Status)
	}
	server.Close()
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	original = nil
	select {
	case id := <-runner.removed:
		t.Fatalf("shutdown removed %s", id)
	default:
	}

	runner.state = docker.State{ID: j.ContainerID, Running: true}
	resumed, err := New(dir, "test-token", "test", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if err := resumed.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := httptest.NewServer(resumed.Handler())
	defer next.Close()
	close(runner.exit)
	base := "/jobs/" + j.ID
	logs := request(t, next, "GET", base+"/logs?sanitize=true&follow=true", nil, "", http.StatusOK)
	if string(logs) != "first\n100%\nlast" {
		t.Fatalf("replayed logs = %q", logs)
	}
	got := decode[models.Job](t, request(t, next, "GET", base, nil, "", http.StatusOK))
	if got.Status != models.Succeeded || got.StartedAt == nil || !got.StartedAt.Equal(*j.StartedAt) {
		t.Fatalf("recovered job = %+v", got)
	}
}

func TestDockerJob(t *testing.T) {
	if os.Getenv("RIGCAST_TEST_DOCKER") != "1" {
		t.Skip("set RIGCAST_TEST_DOCKER=1 on a Linux Docker host")
	}
	runner, err := docker.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	_, server := testServer(t, runner)
	j := submission(t, server, models.Submission{
		Project: "integration", Command: "echo hello; cp input /outputs/result",
		Spec: models.Spec{Image: "alpine:3.22", GPUs: "none", Timeout: "2m"},
	})
	base := "/jobs/" + j.ID
	var actual models.Job
	deadline := time.Now().Add(2*time.Minute + 15*time.Second)
	for time.Now().Before(deadline) {
		actual = decode[models.Job](t, request(t, server, "GET", base, nil, "", http.StatusOK))
		if actual.Status.Terminal() {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if actual.Status != models.Succeeded {
		t.Fatalf("job did not succeed: %+v", actual)
	}
	if logs := request(t, server, "GET", base+"/logs", nil, "", http.StatusOK); !strings.Contains(string(logs), "hello") {
		t.Fatalf("logs = %q", logs)
	}
	if data := request(t, server, "GET", base+"/artifacts/result", nil, "", http.StatusOK); string(data) != "test data" {
		t.Fatalf("artifact = %q", data)
	}
	if state, err := runner.Inspect(t.Context(), actual.ContainerID); err != nil || state.ID != "" {
		t.Fatalf("container not removed: %+v, %v", state, err)
	}
}

package cli

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rigcast/internal/daemon"
	"rigcast/internal/docker"
	"rigcast/internal/models"
)

type stubRunner struct{}

func (stubRunner) EnsureImage(context.Context, string, func() error) error { return nil }

func (stubRunner) Start(_ context.Context, name, dir, datasets string, spec models.Submission, _ int64) (string, error) {
	data, err := os.ReadFile(filepath.Join(datasets, spec.Datasets[0], "unchanged"))
	if err != nil {
		return "", err
	}
	return name, os.WriteFile(filepath.Join(dir, "outputs", "result.json"), data, 0600)
}

func (stubRunner) Inspect(context.Context, string) (docker.State, error) { return docker.State{}, nil }
func (stubRunner) Logs(_ context.Context, _ string, w io.Writer) error {
	_, err := io.WriteString(w, "training\n")
	return err
}
func (stubRunner) Wait(context.Context, string) (int, error) { return 0, nil }
func (stubRunner) Remove(context.Context, string) error      { return nil }

func testCLI(t *testing.T, runner daemon.Runner) {
	t.Helper()
	d, err := daemon.New(t.TempDir(), "test-token", "test", runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	server := httptest.NewServer(d.Handler())
	t.Cleanup(server.Close)
	t.Setenv("RIGCAST_HOST", server.URL)
	t.Setenv("RIGCAST_TOKEN", "test-token")
	dataset := t.TempDir()
	put := func(name, data string) {
		p := filepath.Join(dataset, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	execute := func(args ...string) (string, string) {
		root := New("test")
		var out, stderr bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&stderr)
		root.SetArgs(args)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		if err := root.ExecuteContext(ctx); err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, out.String(), stderr.String())
		}
		return out.String(), stderr.String()
	}
	put("unchanged", `{"result":42}`)
	put("old", "old")
	if got, _ := execute("data", "push", dataset, "samples"); got != "samples: 2 uploaded, 0 deleted\n" {
		t.Fatal(got)
	}
	if err := os.Remove(filepath.Join(dataset, "old")); err != nil {
		t.Fatal(err)
	}
	put("old/new", "different type")
	if got, _ := execute("data", "push", dataset, "samples"); got != "samples: 1 uploaded, 1 deleted\n" {
		t.Fatal(got)
	}
	if got, _ := execute("data", "push", dataset, "samples"); got != "samples: 0 uploaded, 0 deleted\n" {
		t.Fatal(got)
	}
	if got, _ := execute("data", "ls"); !strings.Contains(got, "samples") {
		t.Fatal(got)
	}
	t.Chdir(t.TempDir())
	execute("init")
	out, stderr := execute("run", "--gpus", "none", "--image", "alpine:3.22", "--dataset", "samples", "--",
		`if (echo bad > /data/samples/denied) 2>/dev/null; then exit 42; fi; cp /data/samples/unchanged /outputs/result.json; echo training`)
	if out != "training\n" {
		t.Fatalf("run output: %q", out)
	}
	id := strings.TrimSpace(strings.TrimPrefix(stderr, "Job "))
	execute("pull", id)
	result, err := os.ReadFile(filepath.Join("outputs", id, "result.json"))
	if err != nil || string(result) != `{"result":42}` {
		t.Fatalf("result = %s, %v", result, err)
	}
	if got, _ := execute("ps", "--status", "succeeded"); !strings.Contains(got, id) {
		t.Fatalf("ps output: %q", got)
	}
}

func TestCLIFlow(t *testing.T) { testCLI(t, stubRunner{}) }

func TestDockerCLIFlow(t *testing.T) {
	if os.Getenv("RIGCAST_TEST_DOCKER") != "1" {
		t.Skip("set RIGCAST_TEST_DOCKER=1 on a Linux Docker host")
	}
	runner, err := docker.New(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	testCLI(t, runner)
}

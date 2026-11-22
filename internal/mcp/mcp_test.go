package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"rigcast/internal/client"
)

func TestMCPSubprocess(t *testing.T) {
	if os.Getenv("RIGCAST_MCP_TEST_HELPER") != "1" {
		return
	}
	c, err := client.New(os.Getenv("RIGCAST_HOST"), "test-token")
	if err == nil {
		err = Run(context.Background(), c, "test")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestStdioTools(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/gpu", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, `{"gpus":[],"active_jobs":0}`)
	})
	mux.HandleFunc("POST /api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if metadata := r.FormValue("metadata"); !strings.Contains(metadata, `"command":"echo hello"`) {
			t.Errorf("metadata = %q", metadata)
		}
		f, _, err := r.FormFile("workspace")
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.Close()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"id":"test-job","status":"queued"}`)
	})
	mux.HandleFunc("GET /api/v1/jobs/test-job/logs", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("sanitize") != "true" || q.Get("follow") != "false" || q.Get("tail") != "50" {
			t.Errorf("logs query = %s", q.Encode())
		}
		_, _ = fmt.Fprint(w, "100%\n")
	})
	mux.HandleFunc("GET /api/v1/jobs/test-job/artifacts/{file}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("file") {
		case "big":
			_, _ = fmt.Fprint(w, strings.Repeat("a", artifactLimit+1))
		case "binary":
			_, _ = w.Write([]byte{0, 1, 2})
		default:
			_, _ = fmt.Fprint(w, `{"value":42}`)
		}
	})
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestMCPSubprocess$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "RIGCAST_MCP_TEST_HELPER=1", "RIGCAST_HOST="+httpServer.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	enc := json.NewEncoder(stdin)
	lines := bufio.NewScanner(stdout)
	send := func(message any) {
		if err := enc.Encode(message); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		Structured json.RawMessage `json:"structuredContent"`
		Tools      []struct{ Name string }
		IsError    bool `json:"isError"`
		Content    []struct{ Text string }
	}
	id := 0
	call := func(method string, params any) result {
		t.Helper()
		id++
		send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		var resp struct {
			JSONRPC string
			ID      int
			Result  result
			Error   json.RawMessage
		}
		if !lines.Scan() {
			t.Fatalf("no %s response: %v", method, lines.Err())
		}
		if err := json.Unmarshal(lines.Bytes(), &resp); err != nil || resp.JSONRPC != "2.0" || resp.ID != id || resp.Error != nil {
			t.Fatalf("invalid %s response: %s (%v)", method, lines.Bytes(), err)
		}
		return resp.Result
	}
	call("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": struct{}{},
		"clientInfo": map[string]string{"name": "stdio-test", "version": "1"},
	})
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	var names []string
	for _, tool := range call("tools/list", struct{}{}).Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"cancel_job", "gpu_status", "job_status", "list_artifacts", "list_datasets", "read_artifact", "submit_job", "tail_logs"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v", names)
	}
	tool := func(name string, args map[string]any) result {
		return call("tools/call", map[string]any{"name": name, "arguments": args})
	}
	if got := tool("gpu_status", map[string]any{}); got.IsError || !bytes.Contains(got.Structured, []byte(`"active_jobs":0`)) {
		t.Fatalf("GPU result = %+v", got)
	}
	if got := tool("submit_job", map[string]any{"command": "echo hello", "image": "alpine"}); got.IsError || !bytes.Contains(got.Structured, []byte(`"job_id":"test-job"`)) {
		t.Fatalf("submit result = %+v", got)
	}
	if got := tool("tail_logs", map[string]any{"job_id": "test-job"}); got.IsError || len(got.Content) != 1 || got.Content[0].Text != "100%\n" {
		t.Fatalf("tail result = %+v", got)
	}
	if got := tool("tail_logs", map[string]any{"job_id": "test-job", "lines": 501}); !got.IsError {
		t.Fatalf("tail accepted 501 lines: %+v", got)
	}
	for _, file := range []string{"big", "binary", "small"} {
		got := tool("read_artifact", map[string]any{"job_id": "test-job", "path": file})
		if got.IsError != (file != "small") || len(got.Content) != 1 || (file == "small" && got.Content[0].Text != `{"value":42}`) {
			t.Fatalf("read %s result = %+v", file, got)
		}
	}
	stdin.Close()
	if lines.Scan() {
		t.Fatalf("unexpected stdout: %s", lines.Bytes())
	}
	err = cmd.Wait()
	waited = true
	if err != nil || lines.Err() != nil {
		t.Fatalf("MCP exits with %v (scan: %v): %s", err, lines.Err(), stderr.String())
	}
}

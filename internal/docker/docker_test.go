package docker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"rigcast/internal/models"
)

func TestDockerAPIConfiguration(t *testing.T) {
	dir := t.TempDir()
	created := make(chan container.CreateRequest, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1.44/containers/create", func(w http.ResponseWriter, r *http.Request) {
		var req container.CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if name := r.URL.Query().Get("name"); name != "rigcast-test" {
			t.Errorf("container name = %q", name)
		}
		created <- req
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"Id":"container-id"}`)
	})
	mux.HandleFunc("POST /v1.44/containers/container-id/start", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1.44/containers/container-id/logs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = stdcopy.NewStdWriter(w, stdcopy.Stdout).Write([]byte("out\n"))
		_, _ = stdcopy.NewStdWriter(w, stdcopy.Stderr).Write([]byte("err\n"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	api, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.44"))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	r := &Runner{api}
	spec := models.Submission{Command: "python train.py", Spec: models.Spec{
		Image: "alpine", Workdir: "/workspace/code", GPUs: "0,2", Datasets: []string{"samples"},
	}}
	id, err := r.Start(t.Context(), "rigcast-test", dir, dir, spec, 128<<20)
	if err != nil || id != "container-id" {
		t.Fatalf("start = %q, %v", id, err)
	}
	req := <-created
	if req.Config.Image != "alpine" || req.Config.WorkingDir != "/workspace/code" ||
		!reflect.DeepEqual([]string(req.Config.Entrypoint), []string{"/bin/sh", "-c"}) ||
		!reflect.DeepEqual([]string(req.Config.Cmd), []string{"python train.py"}) {
		t.Fatalf("container config = %+v", req.Config)
	}
	host := req.HostConfig
	if host.Memory != 128<<20 || len(host.Mounts) != 3 ||
		host.Mounts[0].ReadOnly || host.Mounts[1].ReadOnly ||
		!host.Mounts[2].ReadOnly || host.Mounts[2].Target != "/data/samples" ||
		host.Mounts[2].Source != filepath.Join(dir, "samples") {
		t.Fatalf("container mounts/resources = %+v", host)
	}
	gpu := []container.DeviceRequest{{Driver: "nvidia", DeviceIDs: []string{"0", "2"}, Capabilities: [][]string{{"gpu"}}}}
	if !reflect.DeepEqual(host.DeviceRequests, gpu) {
		t.Fatalf("GPU request = %+v", host.DeviceRequests)
	}
	var logs bytes.Buffer
	if err := r.Logs(t.Context(), id, &logs); err != nil || logs.String() != "out\nerr\n" {
		t.Fatalf("container logs = %q, %v", logs.String(), err)
	}
}

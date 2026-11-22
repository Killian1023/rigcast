package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"

	"rigcast/internal/models"
)

const deadlineLabel = "rigcast.deadline"

type Runner struct{ api *client.Client }

func New(ctx context.Context) (*Runner, error) {
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	if _, err := api.Ping(ctx); err != nil {
		api.Close()
		return nil, fmt.Errorf("connect to Docker: %w", err)
	}
	return &Runner{api}, nil
}

func (r *Runner) Close() error { return r.api.Close() }

func (r *Runner) EnsureImage(ctx context.Context, name string, pulling func() error) error {
	_, err := r.api.ImageInspect(ctx, name)
	if !errdefs.IsNotFound(err) {
		return err
	}
	if err := pulling(); err != nil {
		return err
	}
	stream, err := r.api.ImagePull(ctx, name, image.PullOptions{})
	if err != nil {
		return err
	}
	defer stream.Close()
	dec := json.NewDecoder(stream)
	for {
		var event struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if event.Error != "" {
			return errors.New(event.Error)
		}
	}
}

func (r *Runner) Start(ctx context.Context, name, jobDir, datasetsDir string, spec models.Submission, memory int64) (string, error) {
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	labels := map[string]string{"rigcast.managed": "true"}
	if deadline, ok := ctx.Deadline(); ok {
		labels[deadlineLabel] = deadline.UTC().Format(time.RFC3339Nano)
	}
	cfg := &container.Config{
		Image: spec.Image, WorkingDir: spec.Workdir, Env: env, Labels: labels,
		Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{spec.Command},
	}
	host := &container.HostConfig{
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: filepath.Join(jobDir, "workspace"), Target: "/workspace"},
			{Type: mount.TypeBind, Source: filepath.Join(jobDir, "outputs"), Target: "/outputs"},
		},
		Resources: container.Resources{Memory: memory},
	}
	for _, d := range spec.Datasets {
		host.Mounts = append(host.Mounts, mount.Mount{
			Type: mount.TypeBind, Source: filepath.Join(datasetsDir, d), Target: "/data/" + d, ReadOnly: true,
		})
	}
	if spec.GPUs != "none" {
		req := container.DeviceRequest{Driver: "nvidia", Capabilities: [][]string{{"gpu"}}}
		if spec.GPUs == "all" {
			req.Count = -1
		} else {
			req.DeviceIDs = strings.Split(spec.GPUs, ",")
		}
		host.DeviceRequests = []container.DeviceRequest{req}
	}
	created, err := r.api.ContainerCreate(ctx, cfg, host, nil, nil, name)
	if err != nil {
		return "", err
	}
	return created.ID, r.api.ContainerStart(ctx, created.ID, container.StartOptions{})
}

type State struct {
	ID       string
	Running  bool
	Deadline time.Time
}

func (r *Runner) Inspect(ctx context.Context, id string) (State, error) {
	info, err := r.api.ContainerInspect(ctx, id)
	if errdefs.IsNotFound(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	if info.Config == nil || info.Config.Labels["rigcast.managed"] != "true" {
		return State{}, fmt.Errorf("container %q is not managed by rigcast", id)
	}
	deadline, _ := time.Parse(time.RFC3339Nano, info.Config.Labels[deadlineLabel])
	return State{ID: info.ID, Running: info.State != nil && info.State.Running, Deadline: deadline}, nil
}

func (r *Runner) Logs(ctx context.Context, id string, w io.Writer) error {
	logs, err := r.api.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
	if err != nil {
		return err
	}
	defer logs.Close()
	_, err = stdcopy.StdCopy(w, w, logs)
	return err
}

func (r *Runner) Wait(ctx context.Context, id string) (int, error) {
	result, failed := r.api.ContainerWait(ctx, id, container.WaitConditionNotRunning)
	select {
	case status := <-result:
		if status.Error != nil {
			return 0, errors.New(status.Error.Message)
		}
		return int(status.StatusCode), nil
	case err := <-failed:
		return 0, err
	}
}

func (r *Runner) Remove(ctx context.Context, id string) error {
	err := r.api.ContainerRemove(ctx, id, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

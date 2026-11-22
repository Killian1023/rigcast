package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"rigcast/internal/config"
	"rigcast/internal/models"
	syncfs "rigcast/internal/sync"
)

func (c *Client) Submit(ctx context.Context, dir, command string, override models.Spec) (models.Job, error) {
	var j models.Job
	if strings.TrimSpace(command) == "" {
		return j, errors.New("command is required")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return j, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return j, err
	}
	defer root.Close()
	spec, err := config.Project(root)
	if err != nil {
		return j, err
	}
	if override.Image != "" {
		spec.Image = override.Image
	}
	if override.GPUs != "" {
		spec.GPUs = override.GPUs
	}
	if override.Datasets != nil {
		spec.Datasets = override.Datasets
	}
	if strings.TrimSpace(spec.Image) == "" {
		return j, errors.New("set an image in rigcast.yaml or supply an image override")
	}
	filter, err := syncfs.WorkspaceFilter(root)
	if err != nil {
		return j, err
	}
	metadata, err := json.Marshal(models.Submission{Project: filepath.Base(dir), Command: command, Spec: spec})
	if err != nil {
		return j, err
	}
	body, err := c.upload(ctx, "/jobs", "workspace", "workspace.tar.gz", map[string]string{"metadata": string(metadata)}, func(w io.Writer) error {
		return syncfs.Pack(w, root, filter)
	})
	if err != nil {
		return j, err
	}
	defer body.Close()
	err = json.NewDecoder(body).Decode(&j)
	return j, err
}

type Transfer struct {
	Uploaded int
	Deleted  int
}

func (c *Client) Push(ctx context.Context, dir, name string) (Transfer, error) {
	var result Transfer
	root, err := os.OpenRoot(dir)
	if err != nil {
		return result, err
	}
	defer root.Close()
	manifest, err := syncfs.Manifest(ctx, root)
	if err != nil {
		return result, err
	}
	diff, err := c.diff(ctx, name, manifest)
	if err != nil {
		return result, err
	}
	if len(manifest) != 0 && len(diff.Upload) == 0 && len(diff.Delete) == 0 {
		return result, nil
	}
	for _, name := range diff.Upload {
		if _, exists := manifest[name]; !exists {
			return result, errors.New("daemon requested a file outside the submitted manifest")
		}
	}
	if diff.Delete == nil {
		diff.Delete = []string{}
	}
	remove, err := json.Marshal(diff.Delete)
	if err != nil {
		return result, err
	}
	body, err := c.upload(ctx, "/datasets/"+url.PathEscape(name)+"/sync", "archive", "dataset.tar.gz", map[string]string{"delete": string(remove)}, func(w io.Writer) error {
		return syncfs.PackFiles(w, root, diff.Upload)
	})
	if err != nil {
		return result, err
	}
	defer body.Close()
	err = json.NewDecoder(body).Decode(&result)
	return result, err
}

func (c *Client) Pull(ctx context.Context, id, dir string) error {
	body, err := c.ArtifactArchive(ctx, id)
	if err != nil {
		return err
	}
	defer body.Close()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return syncfs.Extract(dir, body, 1<<40)
}

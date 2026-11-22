package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"rigcast/internal/models"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(host, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(host, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("RIGCAST_HOST must be an http:// or https:// daemon URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("daemon token is required")
	}
	return &Client{
		base: u.String() + "/api/v1", token: token,
		http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("daemon HTTP %d: %s", e.Status, e.Message) }

func (c *Client) request(ctx context.Context, method, path, contentType string, body io.Reader) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if path != "/health" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.Body, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var problem struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &problem) != nil || problem.Error == "" {
		problem.Error = resp.Status
	}
	return nil, &HTTPError{resp.StatusCode, problem.Error}
}

func get[T any](ctx context.Context, c *Client, method, path string) (T, error) {
	var v T
	body, err := c.request(ctx, method, path, "", nil)
	if err != nil {
		return v, err
	}
	defer body.Close()
	err = json.NewDecoder(body).Decode(&v)
	return v, err
}

func (c *Client) Health(ctx context.Context) (models.Health, error) {
	return get[models.Health](ctx, c, http.MethodGet, "/health")
}

func (c *Client) GPU(ctx context.Context) (models.GPUStatus, error) {
	return get[models.GPUStatus](ctx, c, http.MethodGet, "/gpu")
}

func jobPath(id string) string { return "/jobs/" + url.PathEscape(id) }

func (c *Client) Job(ctx context.Context, id string) (models.Job, error) {
	return get[models.Job](ctx, c, http.MethodGet, jobPath(id))
}

func (c *Client) Jobs(ctx context.Context, limit int, status string) ([]models.Job, error) {
	v := url.Values{"limit": {strconv.Itoa(limit)}, "status": {status}}
	return get[[]models.Job](ctx, c, http.MethodGet, "/jobs?"+v.Encode())
}

func (c *Client) Cancel(ctx context.Context, id string) (models.Job, error) {
	return get[models.Job](ctx, c, http.MethodDelete, jobPath(id))
}

func (c *Client) Logs(ctx context.Context, id string, tail int, follow, sanitize bool) (io.ReadCloser, error) {
	v := url.Values{
		"tail": {strconv.Itoa(tail)}, "follow": {strconv.FormatBool(follow)}, "sanitize": {strconv.FormatBool(sanitize)},
	}
	return c.request(ctx, http.MethodGet, jobPath(id)+"/logs?"+v.Encode(), "", nil)
}

func (c *Client) Artifacts(ctx context.Context, id string) ([]models.Artifact, error) {
	return get[[]models.Artifact](ctx, c, http.MethodGet, jobPath(id)+"/artifacts")
}

func (c *Client) Artifact(ctx context.Context, id, name string) (io.ReadCloser, error) {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return c.request(ctx, http.MethodGet, jobPath(id)+"/artifacts/"+strings.Join(parts, "/"), "", nil)
}

func (c *Client) ArtifactArchive(ctx context.Context, id string) (io.ReadCloser, error) {
	return c.request(ctx, http.MethodGet, jobPath(id)+"/artifacts.tar.gz", "", nil)
}

func (c *Client) Datasets(ctx context.Context) ([]models.Dataset, error) {
	return get[[]models.Dataset](ctx, c, http.MethodGet, "/datasets")
}

func (c *Client) diff(ctx context.Context, name string, manifest map[string]string) (models.Diff, error) {
	var diff models.Diff
	data, err := json.Marshal(manifest)
	if err != nil {
		return diff, err
	}
	body, err := c.request(ctx, http.MethodPost, "/datasets/"+url.PathEscape(name)+"/diff", "application/json", bytes.NewReader(data))
	if err != nil {
		return diff, err
	}
	defer body.Close()
	err = json.NewDecoder(body).Decode(&diff)
	return diff, err
}

func (c *Client) upload(ctx context.Context, path, field, filename string, fields map[string]string, pack func(io.Writer) error) (io.ReadCloser, error) {
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan error, 1)
	go func() {
		err := func() error {
			for key, value := range fields {
				if err := form.WriteField(key, value); err != nil {
					return err
				}
			}
			part, err := form.CreateFormFile(field, filename)
			if err != nil {
				return err
			}
			if err := pack(part); err != nil {
				return err
			}
			return form.Close()
		}()
		_ = writer.CloseWithError(err)
		done <- err
	}()
	body, err := c.request(ctx, http.MethodPost, path, form.FormDataContentType(), reader)
	_ = reader.Close()
	packErr := <-done
	if err != nil {
		return nil, err
	}
	if packErr != nil {
		body.Close()
		return nil, packErr
	}
	return body, nil
}

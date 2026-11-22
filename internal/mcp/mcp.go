package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"rigcast/internal/client"
	"rigcast/internal/models"
)

const artifactLimit = 100 * 1024

type jobInput struct {
	JobID string `json:"job_id" jsonschema:"The job ID returned by submit_job."`
}

type submitInput struct {
	Command  string   `json:"command" jsonschema:"Shell command to execute in the Docker container."`
	Workdir  string   `json:"workdir,omitempty" jsonschema:"Local project directory. Defaults to the current working directory."`
	Image    string   `json:"image,omitempty" jsonschema:"Override the Docker image in rigcast.yaml."`
	Datasets []string `json:"datasets,omitempty" jsonschema:"Override the read-only datasets in rigcast.yaml."`
	GPUs     string   `json:"gpus,omitempty" jsonschema:"Override GPU allocation: all, none, or comma-separated device IDs."`
}

type submitResult struct {
	JobID   string        `json:"job_id"`
	Status  models.Status `json:"status"`
	Message string        `json:"message"`
}

type cancelResult struct {
	JobID  string        `json:"job_id"`
	Status models.Status `json:"status"`
}

type logsInput struct {
	jobInput
	Lines *int `json:"lines,omitempty" jsonschema:"Number of lines to read, from 1 to 500. Defaults to 50."`
}

type artifactInput struct {
	jobInput
	Path string `json:"path" jsonschema:"File path relative to the job's /outputs directory."`
}

func Run(ctx context.Context, c *client.Client, version string) error {
	return newServer(c, version).Run(ctx, &sdk.StdioTransport{})
}

func newServer(c *client.Client, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "rigcast", Version: version}, nil)
	addJSON(s, "gpu_status", "Check GPU availability, VRAM usage, utilization, and active job count.", true,
		func(ctx context.Context, _ struct{}) (models.GPUStatus, error) { return c.GPU(ctx) })
	addJSON(s, "list_datasets", "List datasets available as read-only mounts under /data/<name>.", false,
		func(ctx context.Context, _ struct{}) ([]models.Dataset, error) { return c.Datasets(ctx) })
	addJSON(s, "submit_job", "Upload the local workspace and queue a Docker job without waiting for it to finish.", true,
		func(ctx context.Context, in submitInput) (submitResult, error) {
			j, err := c.Submit(ctx, in.Workdir, in.Command, models.Spec{Image: in.Image, GPUs: in.GPUs, Datasets: in.Datasets})
			return submitResult{JobID: j.ID, Status: j.Status, Message: "Job queued. Use job_status or tail_logs to check progress."}, err
		})
	addJSON(s, "job_status", "Check a job's status, exit code, and timestamps.", true,
		func(ctx context.Context, in jobInput) (models.Job, error) { return c.Job(ctx, in.JobID) })
	addText(s, "tail_logs", "Read recent log lines with ANSI codes and progress redraws removed.", func(ctx context.Context, in logsInput) (string, error) {
		lines := 50
		if in.Lines != nil {
			lines = *in.Lines
		}
		if lines < 1 || lines > 500 {
			return "", errors.New("lines must be between 1 and 500")
		}
		body, err := c.Logs(ctx, in.JobID, lines, false, true)
		if err != nil {
			return "", err
		}
		defer body.Close()
		raw, err := readBounded(body, 128*1024)
		return string(raw), err
	})
	addJSON(s, "cancel_job", "Terminate a queued or running job.", true, func(ctx context.Context, in jobInput) (cancelResult, error) {
		j, err := c.Cancel(ctx, in.JobID)
		return cancelResult{JobID: j.ID, Status: j.Status}, err
	})
	addJSON(s, "list_artifacts", "List files written to the job's /outputs directory.", false,
		func(ctx context.Context, in jobInput) ([]models.Artifact, error) { return c.Artifacts(ctx, in.JobID) })
	addText(s, "read_artifact", "Read a UTF-8 text artifact no larger than 100 KiB.", func(ctx context.Context, in artifactInput) (string, error) {
		body, err := c.Artifact(ctx, in.JobID, in.Path)
		if err != nil {
			return "", err
		}
		defer body.Close()
		b, err := readBounded(body, artifactLimit)
		if err != nil {
			return "", err
		}
		text := string(b)
		if !utf8.Valid(b) {
			return "", errors.New("artifact is not UTF-8 text")
		}
		for _, r := range text {
			if unicode.IsControl(r) && r != '\t' && r != '\r' && r != '\n' {
				return "", errors.New("artifact is binary")
			}
		}
		return text, nil
	})
	return s
}

func addJSON[In, Out any](s *sdk.Server, name, description string, structured bool, fn func(context.Context, In) (Out, error)) {
	sdk.AddTool(s, &sdk.Tool{Name: name, Description: description},
		func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
			out, err := fn(ctx, in)
			if err != nil {
				return nil, nil, err
			}
			data, err := json.Marshal(out)
			if err != nil {
				return nil, nil, err
			}
			result := textResult(string(data))
			// MCP structured content is object-only; arrays go in a JSON text block.
			if structured {
				result.StructuredContent = json.RawMessage(data)
			}
			return result, nil, nil
		})
}

func addText[In any](s *sdk.Server, name, description string, fn func(context.Context, In) (string, error)) {
	sdk.AddTool(s, &sdk.Tool{Name: name, Description: description},
		func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
			out, err := fn(ctx, in)
			if err != nil {
				return nil, nil, err
			}
			return textResult(out), nil, nil
		})
}

func textResult(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}

func readBounded(body io.Reader, limit int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(body, limit+1))
	if int64(len(buf)) > limit {
		return nil, fmt.Errorf("response exceeds %d KiB", limit/1024)
	}
	return buf, err
}

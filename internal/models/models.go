package models

import "time"

type Spec struct {
	Image       string            `json:"image" yaml:"image"`
	GPUs        string            `json:"gpus" yaml:"gpus"`
	Datasets    []string          `json:"datasets" yaml:"datasets"`
	Env         map[string]string `json:"env,omitempty" yaml:"env"`
	Workdir     string            `json:"workdir,omitempty" yaml:"workdir"`
	Timeout     string            `json:"timeout,omitempty" yaml:"timeout"`
	MemoryLimit string            `json:"memory_limit,omitempty" yaml:"memory_limit"`
}

type Submission struct {
	Project string `json:"project"`
	Command string `json:"command"`
	Spec
}

type Status string

const (
	Queued    Status = "queued"
	Pulling   Status = "pulling"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

func (s Status) Terminal() bool {
	return s == Succeeded || s == Failed || s == Cancelled
}

type Job struct {
	ID          string     `json:"id"`
	Project     string     `json:"project"`
	Command     string     `json:"command"`
	Image       string     `json:"image"`
	Datasets    []string   `json:"datasets"`
	GPUs        string     `json:"gpus"`
	ContainerID string     `json:"container_id,omitempty"`
	Status      Status     `json:"status"`
	ExitCode    *int       `json:"exit_code"`
	Error       string     `json:"error_msg,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

type GPU struct {
	Index       int    `json:"index"`
	Name        string `json:"name"`
	MemoryUsed  *int   `json:"memory_used_mb"`
	MemoryTotal *int   `json:"memory_total_mb"`
	Utilization *int   `json:"utilization_percent"`
	Temperature *int   `json:"temperature_c"`
}

type GPUStatus struct {
	GPUs       []GPU `json:"gpus"`
	ActiveJobs int   `json:"active_jobs"`
}

type Dataset struct {
	Name      string `json:"name"`
	FileCount int    `json:"file_count"`
	Size      int64  `json:"size_bytes"`
}

type Diff struct {
	Upload []string `json:"upload"`
	Delete []string `json:"delete"`
}

type Artifact struct {
	Path string `json:"path"`
	Size int64  `json:"size_bytes"`
}

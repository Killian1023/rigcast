package store

import (
	"path/filepath"
	"testing"
	"time"

	"rigcast/internal/models"
)

func TestJobLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	job := models.Job{ID: "id", Project: "proj", Command: "exit 42", Image: "alpine", GPUs: "none",
		Datasets: []string{"data"}, Status: models.Queued, CreatedAt: time.Now().UTC()}
	if err := db.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := db.Pulling(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Running(job.ID, "container"); err != nil {
		t.Fatal(err)
	}
	jobs, err := db.List(models.Running, 1)
	if err != nil || len(jobs) != 1 || jobs[0].StartedAt == nil {
		t.Fatalf("running jobs = %#v, %v", jobs, err)
	}
	code := 42
	if err := db.Finish(job.ID, models.Failed, &code, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := db.ActiveCount()
	if err != nil || n != 0 {
		t.Fatalf("active count = %d, %v", n, err)
	}
	j, err := db.Get(job.ID)
	if err != nil || j.Status != models.Failed || j.ExitCode == nil || *j.ExitCode != 42 || j.FinishedAt == nil ||
		j.ContainerID != "container" || len(j.Datasets) != 1 || j.Datasets[0] != "data" || !j.CreatedAt.Equal(job.CreatedAt) {
		t.Fatalf("persisted job = %#v, %v", j, err)
	}
}

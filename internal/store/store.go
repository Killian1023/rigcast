package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"rigcast/internal/models"

	_ "modernc.org/sqlite"
)

const schema = `PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS jobs (
	id TEXT PRIMARY KEY, project TEXT NOT NULL, command TEXT NOT NULL,
	image TEXT NOT NULL, datasets TEXT NOT NULL DEFAULT '[]',
	gpus TEXT NOT NULL DEFAULT 'all', container_id TEXT, status TEXT NOT NULL,
	exit_code INTEGER, error_msg TEXT, created_at DATETIME NOT NULL,
	started_at DATETIME, finished_at DATETIME
);
CREATE INDEX IF NOT EXISTS jobs_created ON jobs(created_at DESC);`

const columns = `id,project,command,image,datasets,gpus,coalesce(container_id,''),status,exit_code,coalesce(error_msg,''),created_at,started_at,finished_at`

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(j models.Job) error {
	datasets, err := json.Marshal(j.Datasets)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO jobs (id,project,command,image,datasets,gpus,status,created_at)
		VALUES (?,?,?,?,?,?,?,?)`, j.ID, j.Project, j.Command, j.Image, string(datasets), j.GPUs, j.Status, j.CreatedAt)
	return err
}

type scanner interface{ Scan(...any) error }

func scan(r scanner) (j models.Job, err error) {
	var datasets string
	err = r.Scan(&j.ID, &j.Project, &j.Command, &j.Image, &datasets, &j.GPUs, &j.ContainerID,
		&j.Status, &j.ExitCode, &j.Error, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if err == nil {
		err = json.Unmarshal([]byte(datasets), &j.Datasets)
	}
	return
}

func (s *Store) Get(id string) (models.Job, error) {
	return scan(s.db.QueryRow(`SELECT `+columns+` FROM jobs WHERE id=?`, id))
}

func (s *Store) List(status models.Status, limit int) ([]models.Job, error) {
	return s.list(`WHERE (?='' OR status=?) ORDER BY created_at DESC,id DESC LIMIT ?`, status, status, limit)
}

const active = ` WHERE status IN ('queued','pulling','running')`

func (s *Store) Active() ([]models.Job, error) { return s.list(active) }

func (s *Store) ActiveCount() (n int, err error) {
	err = s.db.QueryRow(`SELECT count(*) FROM jobs` + active).Scan(&n)
	return
}

func (s *Store) list(clause string, args ...any) ([]models.Job, error) {
	rows, err := s.db.Query(`SELECT `+columns+` FROM jobs `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []models.Job{}
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) Pulling(id string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=? WHERE id=?`+` AND status='queued'`, models.Pulling, id)
	return err
}

func (s *Store) Running(id, container string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=?,container_id=?,started_at=coalesce(started_at,?) WHERE id=? AND status IN ('queued','pulling','running')`,
		models.Running, container, time.Now().UTC(), id)
	return err
}

func (s *Store) Finish(id string, status models.Status, code *int, message string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=?,exit_code=?,error_msg=?,finished_at=? WHERE id=? AND status IN ('queued','pulling','running')`,
		status, code, message, time.Now().UTC(), id)
	return err
}

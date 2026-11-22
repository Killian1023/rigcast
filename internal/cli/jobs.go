package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"rigcast/internal/client"
	"rigcast/internal/models"
	syncfs "rigcast/internal/sync"
)

type ExitError struct {
	Code int
	Job  string
}

func (e *ExitError) Error() string { return fmt.Sprintf("job %s exited with code %d", e.Job, e.Code) }

func shellCommand(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

func runCommand() *cobra.Command {
	var spec models.Spec
	var detach, follow bool
	cmd := &cobra.Command{Use: "run [flags] -- <cmd>", Short: "Submit a workspace job and optionally follow its logs", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := remote()
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("dataset") {
				spec.Datasets = nil
			}
			j, err := c.Submit(cmd.Context(), ".", shellCommand(args), spec)
			if err != nil {
				return err
			}
			if detach || !follow {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), j.ID)
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Job %s\n", j.ID)
			if err := copyLogs(cmd, c, j.ID, 0, true, false); err != nil {
				return err
			}
			j, err = c.Job(cmd.Context(), j.ID)
			if err != nil {
				return err
			}
			if j.Status == models.Succeeded {
				return nil
			}
			if j.ExitCode != nil && *j.ExitCode != 0 {
				return &ExitError{Code: *j.ExitCode, Job: j.ID}
			}
			if j.Error != "" {
				return fmt.Errorf("job %s %s: %s", j.ID, j.Status, j.Error)
			}
			return fmt.Errorf("job %s: %s", j.ID, j.Status)
		},
	}
	cmd.Flags().StringVar(&spec.Image, "image", "", "override Docker image")
	cmd.Flags().StringSliceVar(&spec.Datasets, "dataset", nil, "override mounted datasets (repeatable, empty clears)")
	cmd.Flags().StringVar(&spec.GPUs, "gpus", "", "override GPU allocation: all, none, or device IDs")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "print job ID without following logs")
	cmd.Flags().BoolVarP(&follow, "follow", "f", true, "follow logs until the job exits")
	return cmd
}

func psCommand() *cobra.Command {
	var limit int
	var status string
	cmd := &cobra.Command{Use: "ps", Short: "List recent jobs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := remote()
			if err != nil {
				return err
			}
			jobs, err := c.Jobs(cmd.Context(), limit, status)
			if err != nil {
				return err
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "ID\tSTATUS\tPROJECT\tIMAGE\tDURATION\tCOMMAND")
			for _, j := range jobs {
				dur := "-"
				if j.StartedAt != nil {
					end := time.Now()
					if j.FinishedAt != nil {
						end = *j.FinishedAt
					}
					dur = end.Sub(*j.StartedAt).Round(time.Second).String()
				}
				command := strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(j.Command)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.Status, j.Project, j.Image, dur, command)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum jobs to display")
	cmd.Flags().StringVar(&status, "status", "", "filter by job status")
	return cmd
}

func logsCommand() *cobra.Command {
	var tail int
	var follow, sanitize bool
	cmd := &cobra.Command{Use: "logs <job_id>", Short: "Print job logs", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := remote()
			if err != nil {
				return err
			}
			return copyLogs(cmd, c, args[0], tail, follow, sanitize)
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 0, "number of lines from the end (0 for all)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow new log output")
	cmd.Flags().BoolVar(&sanitize, "sanitize", false, "remove ANSI sequences and progress redraws")
	return cmd
}

func copyLogs(cmd *cobra.Command, c *client.Client, id string, tail int, follow, sanitize bool) error {
	r, err := c.Logs(cmd.Context(), id, tail, follow, sanitize)
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(cmd.OutOrStdout(), r)
	return err
}

func killCommand() *cobra.Command {
	return &cobra.Command{Use: "kill <job_id>", Short: "Cancel a queued or running job", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := remote()
			if err != nil {
				return err
			}
			j, err := c.Cancel(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), j.ID, j.Status)
			return err
		},
	}
}

func pullCommand() *cobra.Command {
	return &cobra.Command{Use: "pull <job_id> [dest_dir]", Short: "Download and extract job artifacts", Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dir string
			if len(args) == 2 {
				dir = args[1]
			} else {
				if _, err := syncfs.LocalPath(args[0]); err != nil || strings.Contains(args[0], "/") {
					return errors.New("invalid job ID")
				}
				dir = filepath.Join("outputs", args[0])
			}
			c, err := remote()
			if err != nil {
				return err
			}
			if err := c.Pull(cmd.Context(), args[0], dir); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), dir)
			return err
		},
	}
}

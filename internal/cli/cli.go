package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"rigcast/internal/client"
	"rigcast/internal/config"
	"rigcast/internal/mcp"
)

func New(version string) *cobra.Command {
	root := &cobra.Command{
		Use: "rigcast", Short: "Run ML workloads on a private GPU host", Version: version,
		SilenceErrors: true, SilenceUsage: true,
	}
	root.AddCommand(daemonCommand(version), initCommand(), statusCommand(), dataCommand(),
		runCommand(), psCommand(), logsCommand(), killCommand(), pullCommand(), mcpCommand(version))
	return root
}

func mcpCommand(version string) *cobra.Command {
	return &cobra.Command{Use: "mcp", Short: "Serve MCP tools over stdio", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := remote()
			if err != nil {
				return err
			}
			return mcp.Run(cmd.Context(), c, version)
		},
	}
}

func remote() (*client.Client, error) {
	c, err := config.Global()
	if err != nil {
		return nil, err
	}
	return client.New(c.Host, c.Token)
}

func initCommand() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "Create rigcast.yaml", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := config.Init("."); err != nil {
				return err
			}
			cmd.Println("Created rigcast.yaml")
			return nil
		},
	}
}

func statusCommand() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show daemon and GPU status", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := remote()
			if err != nil {
				return err
			}
			h, err := c.Health(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Daemon: %s (%s)\n", h.Status, h.Version)
			state, err := c.GPU(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Active jobs: %d\n", state.ActiveJobs)
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "GPU\tNAME\tUSED MB\tTOTAL MB\tUTIL %\tTEMP C")
			for _, gpu := range state.GPUs {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", gpu.Index, gpu.Name, measurement(gpu.MemoryUsed),
					measurement(gpu.MemoryTotal), measurement(gpu.Utilization), measurement(gpu.Temperature))
			}
			return w.Flush()
		},
	}
}

func measurement(v *int) string {
	if v == nil {
		return "-"
	}
	return strconv.Itoa(*v)
}

func table(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0) }

func dataCommand() *cobra.Command {
	data := &cobra.Command{Use: "data", Short: "Manage remote read-only datasets"}
	data.AddCommand(
		&cobra.Command{Use: "push <local_dir> <name>", Short: "Sync changed files and remove remote files absent locally", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := remote()
				if err != nil {
					return err
				}
				result, err := c.Push(cmd.Context(), args[0], args[1])
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d uploaded, %d deleted\n", args[1], result.Uploaded, result.Deleted)
				return nil
			},
		},
		&cobra.Command{Use: "ls", Short: "List remote datasets", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				c, err := remote()
				if err != nil {
					return err
				}
				items, err := c.Datasets(cmd.Context())
				if err != nil {
					return err
				}
				w := table(cmd.OutOrStdout())
				fmt.Fprintln(w, "NAME\tFILES\tSIZE (BYTES)")
				for _, item := range items {
					fmt.Fprintf(w, "%s\t%d\t%d\n", item.Name, item.FileCount, item.Size)
				}
				return w.Flush()
			},
		},
	)
	return data
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

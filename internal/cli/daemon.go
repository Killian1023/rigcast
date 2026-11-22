package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"rigcast/internal/daemon"
	"rigcast/internal/docker"
)

func daemonCommand(version string) *cobra.Command {
	var bind, dir, token string
	cmd := &cobra.Command{Use: "daemon", Short: "Start the Linux GPU daemon", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(token) == "" {
				return errors.New("set RIGCAST_TOKEN or pass --token")
			}
			if runtime.GOOS != "linux" {
				return errors.New("daemon mode requires Linux")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			runner, err := docker.New(ctx)
			cancel()
			if err != nil {
				return err
			}
			defer runner.Close()
			d, err := daemon.New(dir, token, version, runner)
			if err != nil {
				return err
			}
			defer d.Close()
			ln, err := net.Listen("tcp", bind)
			if err != nil {
				return err
			}
			defer ln.Close()
			if err := d.Recover(cmd.Context()); err != nil {
				return err
			}
			server := &http.Server{
				Handler:           d.Handler(),
				ReadHeaderTimeout: 5 * time.Second,
				IdleTimeout:       time.Minute,
				BaseContext:       func(net.Listener) context.Context { return cmd.Context() },
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "rigcast: listening on %s\n", ln.Addr())
			done := make(chan error, 1)
			go func() { done <- server.Serve(ln) }()
			select {
			case err := <-done:
				return err
			case <-cmd.Context().Done():
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := server.Shutdown(ctx); err != nil {
					_ = server.Close()
				}
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&bind, "bind", envOr("RIGCAST_BIND", "0.0.0.0:7474"), "HTTP listen address")
	cmd.Flags().StringVar(&dir, "data-dir", envOr("RIGCAST_DATA_DIR", "/var/lib/rigcast"), "daemon state directory")
	cmd.Flags().StringVar(&token, "token", "", "bearer token (prefer RIGCAST_TOKEN)")
	cmd.PreRun = func(cmd *cobra.Command, _ []string) {
		if !cmd.Flags().Changed("token") {
			token = os.Getenv("RIGCAST_TOKEN")
		}
	}
	return cmd
}

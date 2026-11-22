package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"rigcast/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.New(version).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "rigcast:", err)
		var exit *cli.ExitError
		switch {
		case errors.As(err, &exit) && exit.Code > 0 && exit.Code <= 255:
			os.Exit(exit.Code)
		case errors.Is(ctx.Err(), context.Canceled):
			os.Exit(130)
		default:
			os.Exit(1)
		}
	}
}

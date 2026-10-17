// Command med-engine processes queued CDR files (workers, post-actions, recovery and the scheduler).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"dxmed/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, "med-engine", app.OSEnv()); err != nil {
		fmt.Fprintln(os.Stderr, "med-engine:", err)
		os.Exit(1)
	}
}

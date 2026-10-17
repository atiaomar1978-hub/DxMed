// Command med-collector watches the source directories of the active streams and queues complete files.
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
	if err := app.Run(ctx, "med-collector", app.OSEnv()); err != nil {
		fmt.Fprintln(os.Stderr, "med-collector:", err)
		os.Exit(1)
	}
}

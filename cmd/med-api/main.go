// Command med-api serves the DxMed REST API over HTTPS.
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
	if err := app.Run(ctx, "med-api", app.OSEnv()); err != nil {
		fmt.Fprintln(os.Stderr, "med-api:", err)
		os.Exit(1)
	}
}

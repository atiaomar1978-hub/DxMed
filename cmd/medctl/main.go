// Command medctl administers DxMed (migrations, bootstrap, seed, publish, users, sample files, status).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"dxmed/internal/ctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := ctl.Run(ctx, os.Args[1:], ctl.OSEnv())
	stop()
	os.Exit(code)
}

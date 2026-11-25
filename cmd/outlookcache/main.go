// Command outlookcache reads the local mail cache of new Outlook for Mac.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/stn1slv/outlookcache/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop() // a second signal now ends the process at once
	}()
	code := cli.Execute(ctx, version, os.Args[1:])
	stop()
	os.Exit(code)
}

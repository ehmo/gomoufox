package main

import (
	"context"
	"os"

	"github.com/ehmo/gomoufox/internal/cli"
)

func Main(ctx context.Context, args []string, streams cli.Streams) int {
	runner := cli.Runner{}
	return runner.Run(ctx, args, streams)
}

var (
	mainArgs    = func() []string { return os.Args[1:] }
	mainExit    = os.Exit
	mainStreams = func() cli.Streams {
		return cli.Streams{
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		}
	}
)

func main() {
	mainExit(Main(context.Background(), mainArgs(), mainStreams()))
}

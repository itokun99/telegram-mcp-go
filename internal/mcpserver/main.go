package mcpserver

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/itokun99/telegram-mcp-go/internal/config"
)

// Main is the process entry point: parse flags, load the environment, build
// the server from DefaultRegistry and serve it. It returns the process exit
// code and never calls os.Exit itself.
//
// The --dry-run flag prints the served tool list as JSON to stdout and exits
// 0 without connecting to Telegram or starting a transport.
func Main(args []string, stdout, stderr io.Writer) int {
	return mainWith(config.OS{}, DefaultRegistry, args, stdout, stderr)
}

// mainWith is Main with an injectable environment and registry for tests.
func mainWith(src config.EnvSource, reg *Registry, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("telegram-mcp-go", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print the served tool list as JSON and exit without connecting to Telegram")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(src)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	srv, err := Build(reg, OptionsFromConfig(cfg))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if *dryRun {
		if err := srv.DryRun(stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

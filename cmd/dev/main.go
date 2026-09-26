package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"dev-orchestrator/internal/cli"
	"dev-orchestrator/internal/ui"
	"dev-orchestrator/internal/workflow"
)

var version = "development"

func main() { os.Exit(run()) }
func run() int {
	cwd, err := os.Getwd()
	if err != nil {
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan struct{})
	signalDone := make(chan os.Signal, 1)
	go func() {
		select {
		case sig := <-signals:
			signalDone <- sig
			status := 130
			if sig == syscall.SIGTERM {
				status = 143
			}
			cancel(workflow.Interruption{Status: status})
		case <-done:
		}
	}()
	code := (cli.App{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Home: home, Cwd: cwd, Env: os.Environ(), Interactive: ui.IsTTY(os.Stdin), Version: version}).Run(ctx, os.Args[1:])
	close(done)
	select {
	case sig := <-signalDone:
		if sig == syscall.SIGTERM {
			return 143
		}
		return 130
	default:
		return code
	}
}

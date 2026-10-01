// Command tqwebp converts, inspects and remuxes local images using tqwebp's public APIs.
package main

import (
	"context"
	"m31labs.dev/tqwebp/internal/cli"
	"os"
	"os/signal"
)

func main() { os.Exit(mainCode()) }
func mainCode() int {
	ignoreBrokenPipe()
	return withInterruptHandler(func(ctx context.Context) int {
		terminal := false
		if info, e := os.Stdout.Stat(); e == nil {
			terminal = info.Mode()&os.ModeCharDevice != 0
		}
		return cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, terminal)
	})
}

func withInterruptHandler(run func(context.Context) int) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	done := make(chan struct{})
	defer close(done)
	go func() {
		count := 0
		for {
			select {
			case <-done:
				return
			case <-interrupts:
				count++
				if count == 1 {
					cancel()
				} else {
					os.Exit(130)
				}
			}
		}
	}()
	return run(ctx)
}

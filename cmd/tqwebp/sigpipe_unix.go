//go:build linux || darwin

package main

import (
	"os/signal"
	"syscall"
)

// Return broken-pipe write errors through the CLI's I/O exit category.
func ignoreBrokenPipe() { signal.Ignore(syscall.SIGPIPE) }

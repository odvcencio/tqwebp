//go:build !linux && !darwin

package main

func ignoreBrokenPipe() {}

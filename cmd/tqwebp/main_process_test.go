//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"io"
	"m31labs.dev/tqwebp/internal/cli"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type blockingNotifyingReader struct {
	ctx     context.Context
	ready   *os.File
	blocked bool
}

func (r *blockingNotifyingReader) Read([]byte) (int, error) {
	if _, e := r.ready.Write([]byte{'R'}); e != nil {
		return 0, e
	}
	if !r.blocked {
		<-r.ctx.Done()
		return 0, r.ctx.Err()
	}
	go func() { <-r.ctx.Done(); _, _ = r.ready.Write([]byte{'C'}) }()
	select {}
}

// This helper exists only in the test executable, never in the shipped command.
func TestInterruptChild(t *testing.T) {
	mode := os.Getenv("TQWEBP_INTERRUPT_TEST_CHILD")
	if mode == "" {
		return
	}
	ready := os.NewFile(3, "readiness")
	defer ready.Close()
	code := withInterruptHandler(func(ctx context.Context) int {
		reader := &blockingNotifyingReader{ctx: ctx, ready: ready, blocked: mode == "blocked"}
		return cli.Run(ctx, []string{"-", "-o", "-", "--json"}, reader, os.Stdout, os.Stderr, false)
	})
	os.Exit(code)
}
func runInterruptProcess(t *testing.T, blocked bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	read, write, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer read.Close()
	defer write.Close()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptChild$")
	mode := "cooperative"
	if blocked {
		mode = "blocked"
	}
	cmd.Env = append(os.Environ(), "TQWEBP_INTERRUPT_TEST_CHILD="+mode)
	cmd.ExtraFiles = []*os.File{write}
	var out, errout bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	_ = write.Close()
	if e = read.SetReadDeadline(time.Now().Add(8 * time.Second)); e != nil {
		t.Fatal(e)
	}
	var event [1]byte
	if _, e = io.ReadFull(read, event[:]); e != nil || event[0] != 'R' {
		t.Fatal("reader not reached", e, event)
	}
	if e = cmd.Process.Signal(os.Interrupt); e != nil {
		t.Fatal(e)
	}
	if blocked {
		if _, e = io.ReadFull(read, event[:]); e != nil || event[0] != 'C' {
			t.Fatal("first signal did not cancel", e, event)
		}
		if e = cmd.Process.Signal(os.Interrupt); e != nil {
			t.Fatal(e)
		}
	}
	e = cmd.Wait()
	waited = true
	exit, ok := e.(*exec.ExitError)
	if !ok || exit.ExitCode() != 130 {
		t.Fatal("exit", e, "stderr", errout.String())
	}
	if out.Len() != 0 {
		t.Fatal("signal diagnostics corrupted binary stdout")
	}
	if !blocked && !bytes.Contains(errout.Bytes(), []byte(`"category":"interrupt"`)) {
		t.Fatal(errout.String())
	}
	if ctx.Err() != nil {
		t.Fatal("supervisor timeout, not signal handler, ended child")
	}
}
func TestFirstInterruptCancelsCooperativeInput(t *testing.T) { runInterruptProcess(t, false) }
func TestSecondInterruptTerminatesBlockedInput(t *testing.T) { runInterruptProcess(t, true) }

func TestMainBrokenPipeChild(t *testing.T) {
	path := os.Getenv("TQWEBP_MAIN_PIPE_TEST_INPUT")
	if path == "" {
		return
	}
	os.Args = []string{os.Args[0], path, "-o", "-", "--json", "--method", "1"}
	os.Exit(mainCode())
}
func TestBrokenStdoutPipeReturnsIOExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Reuse an existing repository fixture; no native decoder/process is needed.
	path, e := filepath.Abs("../../testdata/corpus/edge_small_64.png")
	if e != nil {
		t.Fatal(e)
	}
	read, write, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	read.Close()
	defer write.Close()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainBrokenPipeChild$")
	cmd.Env = append(os.Environ(), "TQWEBP_MAIN_PIPE_TEST_INPUT="+path)
	cmd.Stdout = write
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	e = cmd.Run()
	exit, ok := e.(*exec.ExitError)
	if !ok || exit.ExitCode() != 5 || !bytes.Contains(diagnostics.Bytes(), []byte(`"category":"io"`)) {
		t.Fatal(e, diagnostics.String())
	}
	if ctx.Err() != nil {
		t.Fatal("supervisor killed child")
	}
}

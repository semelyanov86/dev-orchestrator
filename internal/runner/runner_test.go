package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerHelper(t *testing.T) {
	mode := os.Getenv("DEV_TEST_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "input":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(2)
		}
		fmt.Print(string(b))
		fmt.Print("|key=" + os.Getenv("OPENROUTER_API_KEY"))
	case "large":
		fmt.Print(strings.Repeat("x", 10000))
	case "descendant":
		child := exec.Command(os.Args[0], "-test.run=TestRunnerHelper")
		child.Env = append(os.Environ(), "DEV_TEST_HELPER=block")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		f, err := os.OpenFile(os.Getenv("DEV_TEST_READY"), os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		fmt.Fprintf(f, "%d\n", child.Process.Pid)
		if err := f.Close(); err != nil {
			os.Exit(2)
		}
		select {}
	case "block":
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(0)
}
func TestRunInputOutputAndEnv(t *testing.T) {
	r := New()
	req := Request{Command: os.Args[0], Args: []string{"-test.run=TestRunnerHelper"}, Input: "$(must remain literal)\nmultiline", Timeout: 5 * time.Second, Env: append(os.Environ(), "DEV_TEST_HELPER=input", "OPENROUTER_API_KEY=private-key")}
	result, err := r.Run(t.Context(), req)
	if err != nil || result.Stdout != req.Input+"|key=" {
		t.Fatalf("input/env contract %q %v", result.Stdout, err)
	}
	req.Env = append(os.Environ(), "DEV_TEST_HELPER=large")
	r.MaxOutput = 100
	result, err = r.Run(t.Context(), req)
	if err != nil || len(result.Stdout) != 100 || !result.Truncated {
		t.Fatalf("bounded drained output %+v %v", result, err)
	}
}
func TestRunCancellationKillsDescendants(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "ready")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	ready := make(chan int, 1)
	go func() {
		f, err := os.Open(fifo)
		if err != nil {
			ready <- 0
			return
		}
		defer func() { _ = f.Close() }()
		var pid int
		if _, err := fmt.Fscan(f, &pid); err != nil {
			ready <- 0
			return
		}
		ready <- pid
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := New().Run(ctx, Request{Command: os.Args[0], Args: []string{"-test.run=TestRunnerHelper"}, Timeout: 5 * time.Second, Env: append(os.Environ(), "DEV_TEST_HELPER=descendant", "DEV_TEST_READY="+fifo)})
		done <- err
	}()
	pid := <-ready
	if pid == 0 {
		t.Fatal("helper did not start")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancellation lost")
	}
	// A killed child may briefly remain a zombie until adopted; it must not be running.
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err == nil {
		end := strings.LastIndex(string(data), ")")
		state := strings.Fields(string(data)[end+1:])
		if len(state) == 0 || state[0] != "Z" {
			t.Fatalf("descendant survived: %s", data)
		}
	}
}

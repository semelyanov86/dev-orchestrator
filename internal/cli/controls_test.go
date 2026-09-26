package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"dev-orchestrator/internal/artifacts"
	"dev-orchestrator/internal/safety"
	"dev-orchestrator/internal/ui"
	"dev-orchestrator/internal/workflow"
)

func TestQuestionBrokerEOFAndCancellation(t *testing.T) {
	for _, eof := range []bool{true, false} {
		name := "cancellation"
		if eof {
			name = "EOF"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				broker := &questionBroker{answers: make(chan string), done: make(chan struct{})}
				u := ui.New(io.Discard, io.Discard, safety.New(), ui.Options{})
				done := make(chan error, 1)
				go func() { _, err := broker.ask(ctx, u, "scope?"); done <- err }()
				synctest.Wait()
				if !broker.waiting.Load() {
					t.Fatal("question did not start")
				}
				if eof {
					(App{Interactive: true}).controls(ctx, ui.NewInput(strings.NewReader(""), true, u), make(chan workflow.Update), broker, u)
				} else {
					cancel()
				}
				err := <-done
				if eof && !errors.Is(err, io.EOF) {
					t.Fatalf("EOF lost: %v", err)
				}
				if !eof && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel lost: %v", err)
				}
			})
		})
	}
}

type notifyingReader struct {
	io.ReadCloser
	started chan struct{}
}

func (r *notifyingReader) Read(b []byte) (int, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	return r.ReadCloser.Read(b)
}

func TestTaskFileStdinCancellationArtifacts(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("synthetic git: %v %s", err, out)
	}
	home := t.TempDir()
	read, write := io.Pipe()
	defer func() { _ = write.Close() }()
	source := &notifyingReader{ReadCloser: read, started: make(chan struct{})}
	var out, stderr bytes.Buffer
	ctx, cancel := context.WithCancelCause(t.Context())
	done := make(chan int, 1)
	go func() {
		done <- (App{In: source, Out: &out, Err: &stderr, Cwd: dir, Home: home}).Run(ctx, []string{"feature", "--task-file", "-", "--no-jev"})
	}()
	<-source.started
	cancel(workflow.Interruption{Status: 143})
	if code := <-done; code != 143 {
		t.Fatalf("cancelled stdin code=%d: %s", code, stderr.String())
	}
	entries, err := artifacts.History(artifacts.Root(home))
	if err != nil || len(entries) != 1 {
		t.Fatalf("missing cancellation artifact: %v", err)
	}
	path, err := artifacts.Find(artifacts.Root(home), entries[0].RunID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(path, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Outcome  string `json:"outcome"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Outcome != "cancelled" || meta.ExitCode != 143 {
		t.Fatalf("artifact status: %+v", meta)
	}
}

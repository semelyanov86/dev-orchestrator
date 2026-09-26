package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"dev-orchestrator/internal/safety"
)

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }
func (b *lockedBuffer) Len() int       { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.Len() }

func TestProgressBeforeCompletionAndHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out, stderr lockedBuffer
		u := New(&out, &stderr, safety.New("synthetic-secret"), Options{})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { u.Run(ctx); close(done) }()
		u.Emit(Event{Kind: "start", ID: "01", Name: "investigation", Actor: "Codex", Access: "read_only"})
		synctest.Wait()
		if !strings.Contains(stderr.String(), "investigation") {
			t.Fatal("start not visible while stage blocked")
		}
		if out.Len() != 0 {
			t.Fatal("progress contaminated stdout")
		}
		time.Sleep(31 * time.Second)
		synctest.Wait()
		if !strings.Contains(stderr.String(), "ожидаем ответ") {
			t.Fatal("quiet long stage lacks heartbeat")
		}
		u.Emit(Event{Kind: "finish", ID: "01", Name: "investigation", Status: "completed"})
		before := stderr.Len()
		time.Sleep(31 * time.Second)
		synctest.Wait()
		if stderr.Len() != before {
			t.Fatal("heartbeat continues after finish")
		}
		if err := u.Final("success synthetic-secret\x1b[2J"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "synthetic-secret") || strings.Contains(out.String(), "\x1b") {
			t.Fatal("unsafe final output")
		}
		cancel()
		<-done
	})
}
func TestOutputModes(t *testing.T) {
	for _, tt := range []struct {
		name      string
		options   Options
		heartbeat bool
		ordinary  bool
		ansi      bool
	}{{name: "plain", ordinary: true, heartbeat: true}, {name: "quiet", options: Options{Quiet: true}}, {name: "verbose", options: Options{Verbose: true}, ordinary: true, heartbeat: true}, {name: "tty", options: Options{StderrTTY: true}, ordinary: true, heartbeat: true, ansi: true}, {name: "dumb", options: Options{StderrTTY: true, Dumb: true}, ordinary: true, heartbeat: true}, {name: "no color plain", options: Options{NoColor: true}, ordinary: true, heartbeat: true}} {
		t.Run(tt.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			u := New(&out, &stderr, safety.New(), tt.options)
			u.Emit(Event{Kind: "start", ID: "01", Name: "stage", Actor: "Codex"})
			u.heartbeat(u.Options.Now().Add(31 * time.Second))
			if (strings.Contains(stderr.String(), "stage")) != tt.ordinary {
				t.Fatal("ordinary events mode mismatch")
			}
			if strings.Contains(stderr.String(), "\x1b") && !tt.ansi {
				t.Fatal("ansi in plain mode")
			}
			u.Emit(Event{Kind: "error", Name: "required error"})
			u.Emit(Event{Kind: "question", Name: "required question"})
			if !strings.Contains(stderr.String(), "required error") || !strings.Contains(stderr.String(), "required question") {
				t.Fatal("quiet hid mandatory output")
			}
			if err := u.Final("final"); err != nil {
				t.Fatal(err)
			}
			if out.String() != "final\n" {
				t.Fatal("final stream mismatch")
			}
		})
	}
}
func TestInputMultilineAndCancellation(t *testing.T) {
	var stderr bytes.Buffer
	u := New(io.Discard, &stderr, safety.New(), Options{})
	long := strings.Repeat("a", 100000)
	input := NewInput(strings.NewReader("first\n"+long+"\n:done\nbug\n"), true, u)
	task, err := input.Task()
	if err != nil || task != "first\n"+long {
		t.Fatalf("multiline length=%d err %v", len(task), err)
	}
	line, err := input.Line("workflow")
	if err != nil || line != "bug" {
		t.Fatalf("buffered paste lost %q %v", line, err)
	}
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	input = NewInput(r, true, u)
	ctx, cancel := context.WithCancel(t.Context())
	input.Context = ctx
	cancel()
	_, err = input.Line("cancelled prompt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("input cancellation %v", err)
	}
}

func TestInputPausesStageIndicators(t *testing.T) {
	for _, tty := range []bool{false, true} {
		name := "plain"
		if tty {
			name = "tty"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var stderr lockedBuffer
				u := New(io.Discard, &stderr, safety.New(), Options{StderrTTY: tty, NoColor: true})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				stopped := make(chan struct{})
				go func() { u.Run(ctx); close(stopped) }()
				u.Emit(Event{Kind: "start", ID: "route", Name: "Выбор workflow", Actor: "JEV"})
				time.Sleep(time.Second)
				synctest.Wait()
				read, write := io.Pipe()
				defer func() { _ = write.Close() }()
				input := NewInput(read, true, u)
				input.Context = ctx
				answer := make(chan string, 1)
				go func() {
					line, err := input.Line("Введите номер workflow")
					if err != nil {
						t.Errorf("input: %v", err)
					}
					answer <- line
				}()
				synctest.Wait()
				if !strings.Contains(stderr.String(), "Нужен ваш ответ") {
					t.Error("prompt does not identify user input as the current state")
				}
				before := stderr.String()
				time.Sleep(61 * time.Second)
				synctest.Wait()
				if stderr.String() != before {
					t.Error("provider indicators continue while waiting for user input")
				}
				if _, err := io.WriteString(write, "5\n"); err != nil {
					t.Fatal(err)
				}
				if got := <-answer; got != "5" {
					t.Fatalf("answer = %q, want 5", got)
				}
				synctest.Wait()
				before = stderr.String()
				time.Sleep(31 * time.Second)
				synctest.Wait()
				if stderr.String() == before {
					t.Error("active stage indicators did not resume after user input")
				}
				u.Emit(Event{Kind: "finish", ID: "route", Name: "Выбор workflow", Status: "completed"})
				cancel()
				<-stopped
			})
		})
	}
}

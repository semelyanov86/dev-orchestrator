// Package ui renders ordered progress on stderr and final results on stdout.
package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"dev-orchestrator/internal/safety"
)

type Event struct {
	Kind    string
	ID      string
	Name    string
	Actor   string
	Access  string
	Status  string
	Message string
	Details string
	Round   int
	Limit   int
}
type Sink interface{ Emit(Event) }
type Options struct {
	Quiet     bool
	Verbose   bool
	NoColor   bool
	StderrTTY bool
	StdoutTTY bool
	Dumb      bool
	ASCII     bool
	Now       func() time.Time
	Ticks     <-chan time.Time
}
type active struct {
	event         Event
	started       time.Time
	lastHeartbeat time.Time
}
type UI struct {
	Out        io.Writer
	Err        io.Writer
	Cleaner    safety.Cleaner
	Options    Options
	mu         sync.Mutex
	active     map[string]active
	animated   bool
	writeErr   error
	tick       int
	inputDepth int
}

func New(out, stderr io.Writer, c safety.Cleaner, o Options) *UI {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &UI{Out: out, Err: stderr, Cleaner: c, Options: o, active: map[string]active{}}
}

// IsTTY tests the actual output file; /dev/null is not considered a terminal.
func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func (u *UI) write(w io.Writer, text string) {
	if u.writeErr != nil {
		return
	}
	_, u.writeErr = io.WriteString(w, text)
}
func (u *UI) clear() {
	if u.animated {
		u.write(u.Err, "\r\x1b[2K")
		u.animated = false
	}
}

func (u *UI) Emit(e Event) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	now := u.Options.Now()
	switch e.Kind {
	case "start":
		u.active[e.ID] = active{event: e, started: now, lastHeartbeat: now}
	case "finish":
		delete(u.active, e.ID)
	}
	if u.Options.Quiet && e.Kind != "error" && e.Kind != "question" {
		return
	}
	marker := "[i]"
	switch e.Kind {
	case "start":
		marker = "[>]"
	case "finish":
		if e.Status == "completed" || e.Status == "passed" {
			marker = "[ok]"
		} else {
			marker = "[!]"
		}
	case "error":
		marker = "[x]"
	case "question":
		marker = "[?]"
	}
	label := e.Name
	if e.ID != "" {
		label = e.ID + " " + label
	}
	if e.Actor != "" {
		label += " · " + e.Actor
	}
	if e.Access != "" {
		label += " · " + e.Access
	}
	if e.Round > 0 {
		label += fmt.Sprintf(" · round %d/%d", e.Round, e.Limit)
	}
	if e.Status != "" {
		label += " · " + e.Status
	}
	if e.Message != "" {
		label += "\n  " + e.Message
	}
	if u.Options.Verbose && e.Details != "" {
		label += "\n  " + e.Details
	}
	text := wrap(u.Cleaner.Clean(marker+" "+label), 80)
	if u.Options.StderrTTY && !u.Options.NoColor && !u.Options.Dumb {
		color := "36"
		if marker == "[ok]" {
			color = "32"
		}
		if marker == "[!]" || marker == "[?]" {
			color = "33"
		}
		if marker == "[x]" {
			color = "31"
		}
		text = "\x1b[" + color + "m" + marker + "\x1b[0m" + strings.TrimPrefix(text, marker)
	}
	u.write(u.Err, text+"\n")
}

// Prompt suspends stage indicators until its returned function is called once.
func (u *UI) Prompt(question string) func() {
	u.mu.Lock()
	u.inputDepth++
	u.mu.Unlock()
	u.Emit(Event{Kind: "question", Name: "Нужен ваш ответ", Message: question})
	return func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.inputDepth--
		if u.inputDepth == 0 {
			now := u.Options.Now()
			for id, a := range u.active {
				a.lastHeartbeat = now
				u.active[id] = a
			}
		}
	}
}

// Run owns the ticker and returns only after all indicator work has stopped.
func (u *UI) Run(ctx context.Context) {
	ticks := u.Options.Ticks
	var ticker *time.Ticker
	if ticks == nil {
		ticker = time.NewTicker(time.Second)
		ticks = ticker.C
		defer ticker.Stop()
	}
	defer func() { u.mu.Lock(); defer u.mu.Unlock(); u.clear(); u.active = map[string]active{} }()
	for {
		select {
		case <-ctx.Done():
			return
		case now, ok := <-ticks:
			if !ok {
				return
			}
			u.heartbeat(now)
		}
	}
}

func (u *UI) heartbeat(now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.Options.Quiet || u.inputDepth > 0 || len(u.active) == 0 {
		return
	}
	ids := make([]string, 0, len(u.active))
	for id := range u.active {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	animate := u.Options.StderrTTY && !u.Options.Dumb
	var labels []string
	for _, id := range ids {
		a := u.active[id]
		elapsed := now.Sub(a.started).Round(time.Second)
		label := id + " " + a.event.Actor + " · " + elapsed.String() + " · ожидаем ответ"
		if elapsed >= 30*time.Second {
			label += "; новых сообщений нет"
		}
		labels = append(labels, label)
		if !animate && now.Sub(a.lastHeartbeat) >= 30*time.Second {
			u.write(u.Err, u.Cleaner.Clean("[>] "+label)+"\n")
			a.lastHeartbeat = now
			u.active[id] = a
		}
	}
	if animate {
		u.clear()
		frames := []string{"|", "/", "-", "\\"}
		label := []rune(u.Cleaner.Clean(strings.Join(labels, " · ")))
		width := 80
		if f, ok := u.Err.(*os.File); ok {
			if n, _, err := term.GetSize(int(f.Fd())); err == nil {
				width = n
			}
		}
		if len(label) > max(10, width-4) {
			label = append(label[:max(10, width-7)], '.', '.', '.')
		}
		u.write(u.Err, "\r"+frames[u.tick%len(frames)]+" "+string(label))
		u.tick++
		u.animated = true
	}
}

func (u *UI) Final(text string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clear()
	u.active = map[string]active{}
	u.write(u.Out, u.Cleaner.Clean(text)+"\n")
	return u.writeErr
}
func (u *UI) Error() error { u.mu.Lock(); defer u.mu.Unlock(); return u.writeErr }

// Input has one buffered reader so multiline input and later selections do not lose bytes.
type Input struct {
	Reader      *bufio.Reader
	Source      io.Reader
	Context     context.Context
	Interactive bool
	UI          *UI
}

func NewInput(r io.Reader, interactive bool, u *UI) *Input {
	return &Input{Reader: bufio.NewReader(r), Source: r, Context: context.Background(), Interactive: interactive, UI: u}
}
func (i *Input) Line(question string) (string, error) {
	if !i.Interactive {
		return "", errors.New("needs_input: " + question)
	}
	defer i.UI.Prompt(question)()
	line, err := i.ReadLine(i.Context)
	if errors.Is(err, io.EOF) && line != "" {
		err = nil
	}
	return strings.TrimSpace(line), err
}
func (i *Input) Task() (string, error) {
	if !i.Interactive {
		return "", errors.New("needs_input: provide a workflow and task or --task-file")
	}
	defer i.UI.Prompt("Что нужно сделать? Вставьте текст; завершите отдельной строкой :done")()
	var b strings.Builder
	for {
		line, err := i.ReadLine(i.Context)
		if strings.TrimSpace(line) == ":done" {
			break
		}
		if b.Len()+len(line) > 1<<20 {
			return "", errors.New("task exceeds size limit")
		}
		b.WriteString(line)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// ReadAll reads task-file stdin to EOF with cancellation and a total size limit.
func (i *Input) ReadAll(ctx context.Context, limit int) (string, error) {
	var b strings.Builder
	for {
		line, err := i.ReadLine(ctx)
		if b.Len()+len(line) > limit {
			return "", errors.New("input exceeds size limit")
		}
		b.WriteString(line)
		if errors.Is(err, io.EOF) {
			return b.String(), nil
		}
		if err != nil {
			return "", err
		}
	}
}

func wrap(s string, width int) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		runes := []rune(line)
		for len(runes) > width {
			cut := width
			for j := width; j > width/2; j-- {
				if runes[j] == ' ' {
					cut = j
					break
				}
			}
			lines = append(lines, string(runes[:cut]))
			runes = runes[cut:]
			if len(runes) > 0 && runes[0] == ' ' {
				runes = runes[1:]
			}
		}
		lines = append(lines, string(runes))
	}
	return strings.Join(lines, "\n")
}

// ReadLine preserves buffered paste data and polls real terminals with cancellation.
func (i *Input) ReadLine(ctx context.Context) (string, error) {
	if f, ok := i.Source.(*os.File); ok {
		var b strings.Builder
		for {
			if err := ctx.Err(); err != nil {
				return b.String(), err
			}
			if i.Reader.Buffered() == 0 {
				fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
				n, err := unix.Poll(fds, 100)
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if err != nil {
					return b.String(), err
				}
				if n == 0 {
					continue
				}
				if fds[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0 {
					return b.String(), io.EOF
				}
			}
			ch, err := i.Reader.ReadByte()
			if err != nil {
				return b.String(), err
			}
			b.WriteByte(ch)
			if b.Len() > 1<<20 {
				return "", errors.New("input line exceeds limit")
			}
			if ch == '\n' {
				return b.String(), nil
			}
		}
	}
	if closer, ok := i.Source.(io.Closer); ok {
		type answer struct {
			text string
			err  error
		}
		done := make(chan answer, 1)
		go func() { line, err := i.Reader.ReadString('\n'); done <- answer{text: line, err: err} }()
		select {
		case a := <-done:
			return a.text, a.err
		case <-ctx.Done():
			closeErr := closer.Close()
			<-done
			return "", errors.Join(ctx.Err(), closeErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return i.Reader.ReadString('\n')
}

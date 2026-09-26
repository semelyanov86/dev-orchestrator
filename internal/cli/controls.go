package cli

import (
	"context"
	"io"
	"strings"
	"sync/atomic"

	"dev-orchestrator/internal/ui"
	"dev-orchestrator/internal/workflow"
)

type questionBroker struct {
	waiting atomic.Bool
	answers chan string
	done    chan struct{}
}

func (b *questionBroker) ask(ctx context.Context, u *ui.UI, question string) (string, error) {
	b.waiting.Store(true)
	defer b.waiting.Store(false)
	u.Emit(ui.Event{Kind: "question", Name: "Нужен ваш ответ", Message: question})
	select {
	case answer := <-b.answers:
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-b.done:
		return "", io.EOF
	}
}

// controls and interactive questions share one cancellable buffered reader.
func (a App) controls(ctx context.Context, input *ui.Input, updates chan<- workflow.Update, broker *questionBroker, u *ui.UI) {
	defer close(broker.done)
	if !a.Interactive {
		return
	}
	for {
		line, err := input.ReadLine(ctx)
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		kind := "correction"
		switch {
		case line == ":cancel":
			kind = "cancel"
		case strings.HasPrefix(line, ":update "):
			line = strings.TrimPrefix(line, ":update ")
		case strings.HasPrefix(line, ":restrict "):
			kind = "restrict"
			line = strings.TrimPrefix(line, ":restrict ")
		case strings.HasPrefix(line, ":question "):
			kind = "question"
			line = strings.TrimPrefix(line, ":question ")
		default:
			if broker.waiting.Load() {
				select {
				case broker.answers <- line:
				case <-ctx.Done():
					return
				}
				continue
			}
			u.Emit(ui.Event{Kind: "message", Name: "Для уточнения используйте :update TEXT; для отмены :cancel"})
			continue
		}
		accepted := make(chan struct{})
		select {
		case updates <- workflow.Update{Kind: kind, Text: line, Accepted: accepted}:
			select {
			case <-accepted:
			case <-ctx.Done():
				return
			}
			u.Emit(ui.Event{Kind: "message", Name: "User update получен; применяется на безопасной границе этапа"})
		case <-ctx.Done():
			return
		}
	}
}

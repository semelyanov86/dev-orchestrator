package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dev-orchestrator/internal/agent"
)

func (s *session) ping(ctx context.Context) error {
	var failures []error
	var replies []string
	for _, probe := range []struct{ who, reply string }{{"codex", "ping"}, {"claude", "pong"}} {
		if err := s.updates(); err != nil {
			return err
		}
		if s.generation != 0 {
			return stop("needs_input", "connection test cannot apply task corrections; start a new workflow")
		}
		step, err := s.prepare(ctx, probe.who, "connection-"+probe.who, "ping", agent.ReadOnly, probe.reply)
		if err != nil {
			return err
		}
		result, record, err := s.perform(ctx, step)
		s.run.Steps[step.index] = record
		s.run.Current = recordSnapshot(record, s.run.Current)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s connection test: %w", probe.who, err))
			replies = append(replies, probe.who+": failed")
		} else {
			if err := s.checkEvidence(result.Report); err != nil {
				return err
			}
			for _, requirement := range result.Report.Requirements {
				s.criteria[requirement.ID] = requirement
			}
			s.latest = result.Report
			s.lastGeneration = s.generation
			replies = append(replies, probe.who+": "+result.Report.Markdown)
		}
		s.run.Final = strings.Join(replies, "\n")
		if err := s.persist(); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.Join(failures...)
}

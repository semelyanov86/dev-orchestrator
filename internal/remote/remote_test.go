package remote

import (
	"context"
	"strings"
	"testing"

	"dev-orchestrator/internal/config"
	"dev-orchestrator/internal/runner"
	"dev-orchestrator/internal/safety"
)

type fakeExec struct{ req runner.Request }

func (f *fakeExec) Run(_ context.Context, r runner.Request) (runner.Result, error) {
	f.req = r
	return runner.Result{Stdout: "Authorization: Basic privatevalue", ExitCode: 0}, nil
}
func TestSSHFixedReadOnlyAndQuoting(t *testing.T) {
	f := &fakeExec{}
	r := RemoteExecutor{Exec: f, Cleaner: safety.New()}
	output, err := r.Investigate(t.Context(), config.Server{Host: "production", User: "deploy", Port: 22, ProjectPath: "/srv/app'; touch /tmp/injected; '"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "privatevalue") {
		t.Fatal("ssh output credential leaked")
	}
	command := f.req.Args[len(f.req.Args)-1]
	if !strings.Contains(command, "'\\''") {
		t.Fatal("remote path not quoted")
	}
	for _, term := range []string{"restart", "deploy", "printenv", "env ", "rm "} {
		if strings.Contains(command, term) {
			t.Fatalf("unsafe diagnostic %s", term)
		}
	}
	if _, err := r.Investigate(t.Context(), config.Server{Host: "-oProxyCommand=bad", Port: 22, ProjectPath: "/srv/app"}); err == nil {
		t.Fatal("ssh option injection accepted")
	}
}

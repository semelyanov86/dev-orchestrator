package safety

import (
	"strings"
	"testing"
)

func TestCleanerAndSafeTask(t *testing.T) {
	c := New("known-secret")
	for _, tt := range []struct{ name, input, secret string }{{name: "known key", input: "known-secret", secret: "known-secret"}, {name: "key assignment", input: "API_KEY=not-for-output", secret: "not-for-output"}, {name: "private key", input: "-----BEGIN PRIVATE KEY-----\nprivate data\n-----END PRIVATE KEY-----", secret: "private data"}, {name: "database url", input: "DATABASE_URL=postgres://app:secretpass@db.example/app", secret: "secretpass"}, {name: "basic authorization", input: "Authorization: Basic dXNlcjpwYXNz", secret: "dXNlcjpwYXNz"}, {name: "refresh token", input: `{"refresh_token":"private-refresh"}`, secret: "private-refresh"}, {name: "terminal control", input: "before\x1b[2J\x1b]0;fake title\x07after", secret: "\x1b"}} {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Clean(tt.input)
			if strings.Contains(got, tt.secret) {
				t.Fatalf("secret survives %q", got)
			}
		})
	}
	for _, tt := range []struct {
		name, task string
		safe       bool
	}{{name: "plain task", task: "Fix duplicate appointments", safe: true}, {name: "source code", task: "```go\nfunc main() {}\n```"}, {name: "customer card", task: "Refund Alice Smith card 4111111111111111"}, {name: "email", task: "Customer person@example.org"}, {name: "phone", task: "Call +49 170 1234567"}, {name: "credential", task: "Investigate API_KEY=private-key"}, {name: "logs", task: "2026-09-26T10:12:00 failed stack"}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := SafeTask(tt.task, c); got != tt.safe {
				t.Fatalf("SafeTask=%v want %v", got, tt.safe)
			}
		})
	}
}

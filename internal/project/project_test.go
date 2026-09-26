package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dev-orchestrator/internal/runner"
)

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("synthetic git %v: %v %s", args, err, out)
	}
}
func TestDiscoverDirtyNestedProjectAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-q")
	file := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "tracked.txt")
	gitTest(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	if err := os.WriteFile(file, []byte("staged"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "tracked.txt")
	if err := os.WriteFile(file, []byte("unstaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	inspector := Inspector{Exec: runner.New()}
	p, err := inspector.Discover(t.Context(), nested, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != dir || p.Initial.Staged != 1 || p.Initial.Unstaged != 1 || p.Initial.Untracked != 1 {
		t.Fatalf("dirty inventory %+v", p.Initial)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("changed untracked"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := inspector.Snapshot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID == p.Initial.ID || strings.Join(Changed(p.Initial, after), ",") != "new.txt" {
		t.Fatal("untracked content not included")
	}
	contents, err := os.ReadFile(file)
	if err != nil || string(contents) != "unstaged" {
		t.Fatal("user changes lost")
	}
}
func TestLockAndScope(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	lock, err := AcquireLock(state, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(state, root); err == nil {
		t.Fatal("overlapping writer lock granted")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err = AcquireLock(state, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "/absolute", "escape/file", ".", "docs/..", "./", "alias"} {
		if _, err := ResolveScope(root, path); err == nil {
			t.Fatalf("scope escape accepted %s", path)
		}
	}
	if got, err := ResolveScope(root, "docs/new.md"); err != nil || got != "docs/new.md" {
		t.Fatalf("safe scope %s %v", got, err)
	}
}

func TestRunDiffPreservesDirtyBaseline(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-q")
	path := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(path, []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture")
	if err := os.WriteFile(path, []byte("user security fix\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".")
	if err := os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("user untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	i := Inspector{Exec: runner.New()}
	p, err := i.Discover(t.Context(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user security fix\nrun addition\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new run file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := i.Snapshot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := i.RunDiff(t.Context(), p, after)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff, "committed") || strings.Contains(diff, "existing.txt") || !strings.Contains(diff, "-user security fix") || !strings.Contains(diff, "+run addition") || !strings.Contains(diff, "+new run file") {
		t.Fatalf("run diff lost baseline: %s", diff)
	}
	if p.Initial.Index != after.Index {
		t.Fatal("user staged changes altered")
	}
}

type truncatedGit struct{}

func (truncatedGit) Run(_ context.Context, _ runner.Request) (runner.Result, error) {
	return runner.Result{Stdout: "partial", Truncated: true}, nil
}
func TestTruncatedGitCannotBecomeSnapshot(t *testing.T) {
	if _, err := (Inspector{Exec: truncatedGit{}}).Snapshot(t.Context(), t.TempDir()); err == nil {
		t.Fatal("partial inventory accepted")
	}
}

// Package project discovers the working Git tree and records content-based snapshots.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"dev-orchestrator/internal/runner"
)

type Executor interface {
	Run(context.Context, runner.Request) (runner.Result, error)
}
type Snapshot struct {
	ID        string            `json:"id"`
	Head      string            `json:"head"`
	Branch    string            `json:"branch"`
	Status    string            `json:"status"`
	Files     map[string]string `json:"files"`
	Index     string            `json:"index"`
	Staged    int               `json:"staged"`
	Unstaged  int               `json:"unstaged"`
	Untracked int               `json:"untracked"`
}
type Project struct {
	Root         string
	Instructions string
	HasTaskfile  bool
	Languages    []string
	Initial      Snapshot
	Base         string
	MergeBase    string
	BeforeFiles  map[string]FileContent
}

// FileContent is an in-memory baseline; raw contents are never serialized as run metadata.
type FileContent struct {
	Data    []byte
	Mode    os.FileMode
	Present bool
	Omitted bool
}
type Inspector struct{ Exec Executor }

func (i Inspector) git(ctx context.Context, dir string, args ...string) (string, error) {
	r, err := i.Exec.Run(ctx, runner.Request{Command: "git", Args: args, Dir: dir, Timeout: 10 * time.Second})
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	if r.Truncated {
		return "", errors.New("git output exceeded the inventory limit; snapshot is unavailable")
	}
	return r.Stdout, nil
}

// Discover always asks Git, so nested directories and .git worktree files work.
func (i Inspector) Discover(ctx context.Context, cwd, base string) (Project, error) {
	var p Project
	root, err := i.git(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return p, err
	}
	p.Root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return p, fmt.Errorf("canonical project root: %w", err)
	}
	p.Initial, err = i.Snapshot(ctx, p.Root)
	if err != nil {
		return p, err
	}
	p.BeforeFiles = map[string]FileContent{}
	remaining := 32 << 20
	for name := range p.Initial.Files {
		file, err := readContent(filepath.Join(p.Root, name), remaining)
		if err != nil {
			return p, err
		}
		p.BeforeFiles[name] = file
		remaining -= len(file.Data)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		b, err := os.ReadFile(filepath.Join(p.Root, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return p, err
		}
		if len(b) > 1<<20 {
			return p, errors.New("project instructions exceed size limit")
		}
		p.Instructions += "\n--- " + name + " ---\n" + string(b)
	}
	for _, name := range []string{"Taskfile.yml", "Taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml"} {
		if _, err := os.Stat(filepath.Join(p.Root, name)); err == nil {
			p.HasTaskfile = true
		}
	}
	for _, item := range []struct{ file, language string }{{"go.mod", "Go"}, {"composer.json", "PHP"}, {"package.json", "JavaScript"}, {"tsconfig.json", "TypeScript"}, {"pyproject.toml", "Python"}} {
		if _, err := os.Stat(filepath.Join(p.Root, item.file)); err == nil {
			p.Languages = append(p.Languages, item.language)
		}
	}
	if base != "" {
		if strings.HasPrefix(base, "-") {
			return p, errors.New("invalid review base")
		}
		p.Base, err = i.git(ctx, p.Root, "rev-parse", "--verify", base+"^{commit}")
		if err != nil {
			return p, err
		}
		p.Base = strings.TrimSpace(p.Base)
		p.MergeBase, err = i.git(ctx, p.Root, "merge-base", p.Base, "HEAD")
		if err != nil {
			return p, err
		}
		p.MergeBase = strings.TrimSpace(p.MergeBase)
	}
	return p, nil
}

// Snapshot hashes index, HEAD, tracked and untracked contents, modes and symlink targets.
func (i Inspector) Snapshot(ctx context.Context, root string) (Snapshot, error) {
	s := Snapshot{Files: map[string]string{}}
	head, err := i.git(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		head = "unborn"
	}
	s.Head = strings.TrimSpace(head)
	branch, err := i.git(ctx, root, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		branch = "detached"
	}
	s.Branch = strings.TrimSpace(branch)
	status, err := i.git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return s, err
	}
	s.Status = status
	entries := strings.Split(status, "\x00")
	for n := 0; n < len(entries); n++ {
		entry := entries[n]
		if len(entry) < 3 {
			continue
		}
		if strings.HasPrefix(entry, "??") {
			s.Untracked++
		} else {
			if entry[0] != ' ' {
				s.Staged++
			}
			if entry[1] != ' ' {
				s.Unstaged++
			}
			if entry[0] == 'R' || entry[0] == 'C' || entry[1] == 'R' || entry[1] == 'C' {
				n++
			}
		}
	}
	index, err := i.git(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return s, err
	}
	s.Index = index
	paths, err := i.git(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return s, err
	}
	names := strings.Split(strings.TrimSuffix(paths, "\x00"), "\x00")
	slices.Sort(names)
	names = slices.Compact(names)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", s.Head, s.Branch, index, status)
	for _, name := range names {
		if name == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return s, err
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			s.Files[name] = "deleted"
			fmt.Fprintf(h, "%s\x00deleted\x00", name)
			continue
		}
		if err != nil {
			return s, fmt.Errorf("snapshot %s: %w", name, err)
		}
		fh := sha256.New()
		fmt.Fprintf(fh, "%s\x00", info.Mode())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return s, err
			}
			fmt.Fprint(fh, target)
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return s, err
			}
			_, readErr := io.Copy(fh, f)
			closeErr := f.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return s, err
			}
		case info.IsDir(): // Gitlinks are pinned to their commit and dirty state by status.
		default:
			return s, fmt.Errorf("unsupported file type in snapshot %s", name)
		}
		digest := hex.EncodeToString(fh.Sum(nil))
		s.Files[name] = digest
		fmt.Fprintf(h, "%s\x00%s\x00", name, digest)
	}
	s.ID = hex.EncodeToString(h.Sum(nil))
	return s, nil
}

func Changed(before, after Snapshot) []string {
	keys := map[string]bool{}
	for k := range before.Files {
		keys[k] = true
	}
	for k := range after.Files {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		if before.Files[k] != after.Files[k] {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return names
}

func (i Inspector) Diff(ctx context.Context, root, base string) (string, error) {
	args := []string{"diff", "HEAD"}
	if base != "" {
		args = []string{"diff", base}
	}
	diff, err := i.git(ctx, root, args...)
	if err != nil {
		diff, err = i.git(ctx, root, "diff")
	}
	if err != nil {
		return diff, err
	}
	untracked, err := i.git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return diff, err
	}
	for _, path := range strings.Split(untracked, "\x00") {
		if path == "" {
			continue
		}
		file, err := readContent(filepath.Join(root, path), 2<<20)
		if err != nil {
			return diff, err
		}
		diff += filePatch(path, FileContent{}, file)
	}
	return diff, err
}

func readContent(path string, limit int) (FileContent, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return FileContent{}, nil
	}
	if err != nil {
		return FileContent{}, err
	}
	f := FileContent{Present: true, Mode: info.Mode()}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		f.Data = []byte(target)
		return f, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(min(limit, 2<<20)) {
		f.Omitted = true
		return f, nil
	}
	f.Data, err = os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if strings.ContainsRune(string(f.Data), 0) {
		f.Data = nil
		f.Omitted = true
	}
	return f, nil
}

// RunDiff separates changes made in this run from pre-existing staged/worktree content.
func (i Inspector) RunDiff(ctx context.Context, p Project, after Snapshot) (string, error) {
	var b strings.Builder
	for _, path := range Changed(p.Initial, after) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		current, err := readContent(filepath.Join(p.Root, path), 2<<20)
		if err != nil {
			return "", err
		}
		b.WriteString(filePatch(path, p.BeforeFiles[path], current))
	}
	return b.String(), nil
}

func filePatch(path string, before, after FileContent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	mode := func(f FileContent) uint32 {
		if f.Mode&os.ModeSymlink != 0 {
			return 0120000
		}
		return 0100000 | uint32(f.Mode.Perm())
	}
	switch {
	case !before.Present && after.Present:
		fmt.Fprintf(&b, "new file mode %06o\n", mode(after))
	case before.Present && !after.Present:
		fmt.Fprintf(&b, "deleted file mode %06o\n", mode(before))
	case before.Mode != after.Mode:
		fmt.Fprintf(&b, "old mode %06o\nnew mode %06o\n", mode(before), mode(after))
	}
	if before.Omitted || after.Omitted {
		return b.String() + "Binary, directory, or oversized content omitted; see snapshot hashes.\n"
	}
	oldLabel := "a/" + path
	newLabel := "b/" + path
	if !before.Present {
		oldLabel = "/dev/null"
	}
	if !after.Present {
		newLabel = "/dev/null"
	}
	oldLines := splitLines(before.Data)
	newLines := splitLines(after.Data)
	fmt.Fprintf(&b, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", oldLabel, newLabel, min(1, len(oldLines)), len(oldLines), min(1, len(newLines)), len(newLines))
	for _, set := range []struct {
		prefix string
		lines  []string
		data   []byte
	}{{prefix: "-", lines: oldLines, data: before.Data}, {prefix: "+", lines: newLines, data: after.Data}} {
		for _, line := range set.lines {
			b.WriteString(set.prefix + line + "\n")
		}
		if len(set.data) > 0 && set.data[len(set.data)-1] != '\n' {
			b.WriteString("\\ No newline at end of file\n")
		}
	}
	return b.String()
}
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// Lock prevents concurrent modifying runs in the same canonical worktree.
type Lock struct{ f *os.File }

func AcquireLock(stateRoot, root string) (*Lock, error) {
	dir := filepath.Join(stateRoot, "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(root))
	f, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:])+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := f.Close()
		return nil, errors.Join(errors.New("another modifying run owns this project"), err, closeErr)
	}
	return &Lock{f: f}, nil
}
func (l *Lock) Close() error {
	return errors.Join(syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN), l.f.Close())
}

// ResolveScope rejects traversal and symlinks escaping the working project.
func ResolveScope(root, path string) (string, error) {
	if filepath.IsAbs(path) || path == "." || path == "" {
		return "", errors.New("scope path must be relative and specific")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("scope path escapes project")
	}
	current := filepath.Join(root, clean)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			rel, err := filepath.Rel(root, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || (rel == "." && current == filepath.Join(root, clean)) {
				return "", errors.New("scope symlink escapes project")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("cannot resolve scope")
		}
		current = parent
	}
	return clean, nil
}

// Package artifacts stores private run history outside the working project.
package artifacts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"dev-orchestrator/internal/safety"
)

type Store struct {
	Dir     string
	ID      string
	Cleaner safety.Cleaner
}

func Root(home string) string { return filepath.Join(home, ".local", "state", "dev-agent", "runs") }

func New(root, project string, cleaner safety.Cleaner) (*Store, error) {
	actual := root
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(actual)
		if err == nil {
			actual = resolved
			for n := len(tail) - 1; n >= 0; n-- {
				actual = filepath.Join(actual, tail[n])
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		tail = append(tail, filepath.Base(actual))
		parent := filepath.Dir(actual)
		if parent == actual {
			return nil, err
		}
		actual = parent
	}
	if rel, err := filepath.Rel(project, actual); err != nil || (rel != ".." && !strings.HasPrefix(rel, "../")) {
		return nil, errors.New("artifacts directory must be outside the working project")
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
	project = regexp.MustCompile(`[^a-zA-Z0-9._-]+`).ReplaceAllString(filepath.Base(project), "_")
	if project == "." || project == ".." || project == "" {
		project = "project"
	}
	dir := filepath.Join(root, project, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create artifacts: %w", err)
	}
	return &Store{Dir: dir, ID: id, Cleaner: cleaner}, nil
}

func (s *Store) Write(name, contents string) error {
	return s.write(name, []byte(s.Cleaner.Clean(contents)))
}

// WriteJSON cleans string values before encoding, preserving valid JSON and schema types.
func (s *Store) WriteJSON(name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode artifact: %w", err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	b, err = json.MarshalIndent(cleanJSON(v, s.Cleaner), "", "  ")
	if err != nil {
		return err
	}
	return s.write(name, append(b, '\n'))
}
func cleanJSON(v any, c safety.Cleaner) any {
	switch v := v.(type) {
	case string:
		return c.Clean(v)
	case []any:
		for i := range v {
			v[i] = cleanJSON(v[i], c)
		}
	case map[string]any:
		for k, item := range v {
			v[k] = cleanJSON(item, c)
		}
	}
	return v
}
func (s *Store) write(name string, b []byte) error {
	clean := filepath.Clean(name)
	if filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("artifact path escapes run")
	}
	path := filepath.Join(s.Dir, clean)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".artifact-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(b); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish artifact: %w", err)
	}
	return nil
}

type Entry struct {
	RunID     string    `json:"run_id"`
	Project   string    `json:"project"`
	Workflow  string    `json:"workflow"`
	Outcome   string    `json:"outcome"`
	StartedAt time.Time `json:"started_at"`
	Dir       string    `json:"-"`
}

func History(root string) ([]Entry, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "*", "run.json"))
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(paths))
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(b) > 16<<20 {
			return nil, errors.New("run metadata exceeds limit")
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("read run metadata: %w", err)
		}
		e.Dir = filepath.Dir(path)
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b Entry) int { return b.StartedAt.Compare(a.StartedAt) })
	return entries, nil
}
func Find(root, id string) (string, error) {
	if !regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-f0-9]{12}$`).MatchString(id) {
		return "", errors.New("invalid run id")
	}
	entries, err := History(root)
	if err != nil {
		return "", err
	}
	var dir string
	for _, e := range entries {
		if e.RunID == id {
			if dir != "" {
				return "", errors.New("ambiguous run id")
			}
			dir = e.Dir
		}
	}
	if dir == "" {
		return "", errors.New("run not found")
	}
	return dir, nil
}

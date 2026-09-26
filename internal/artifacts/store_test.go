package artifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dev-orchestrator/internal/safety"
)

func TestPrivateSanitizedArtifacts(t *testing.T) {
	root := t.TempDir()
	s, err := New(root, t.TempDir(), safety.New("known-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteJSON("steps/01/report.json", map[string]string{"output": "known-secret \x1b[2J DATABASE_URL=postgres://app:password@db/app"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "steps/01/report.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "known-secret") || strings.Contains(string(b), "password@") {
		t.Fatal("unsafe artifact")
	}
	var v map[string]string
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal("redaction corrupted JSON")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("public artifact permissions")
	}
	if err := s.Write("../escape", "no"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := Find(root, "../../etc/passwd"); err == nil {
		t.Fatal("show path traversal accepted")
	}
}

func TestArtifactsCannotWriteInsideProject(t *testing.T) {
	project := t.TempDir()
	if _, err := New(filepath.Join(project, "state"), project, safety.New()); err == nil {
		t.Fatal("artifacts wrote into project")
	}
	if _, err := os.Stat(filepath.Join(project, "state")); !os.IsNotExist(err) {
		t.Fatal("rejected artifacts mutated project")
	}
}

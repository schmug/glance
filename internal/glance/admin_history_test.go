package glance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH, skipping")
	}
}

func TestGitHistoryInitsAndCommits(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	historyDir := tmp + "/.glance-history"

	realCfg := tmp + "/glance.yml"
	if err := os.WriteFile(realCfg, []byte("pages: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h, err := openGitHistory(historyDir, []string{realCfg})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := h.recordInitial(); err != nil {
		t.Fatalf("initial: %v", err)
	}

	newContents := []byte("pages:\n  - name: X\n")
	if err := h.commitEdit(realCfg, newContents,
		gitCommitter{Email: "you@example.com", Name: "You"},
		"edit glance.yml · 2026-01-01T00:00:00Z · you@example.com"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	mirror := h.mirrorPath(realCfg)
	got, err := os.ReadFile(mirror)
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if string(got) != string(newContents) {
		t.Fatalf("mirror mismatch: got %q", got)
	}

	entries, err := h.log(10)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(entries))
	}
	if !strings.Contains(entries[0].Message, "edit") {
		t.Fatalf("newest message: got %q", entries[0].Message)
	}
	_ = filepath.Base // silence unused
}

func TestGitHistoryRollback(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	realCfg := tmp + "/glance.yml"
	_ = os.WriteFile(realCfg, []byte("pages: []\n"), 0o600)
	h, err := openGitHistory(tmp+"/.glance-history", []string{realCfg})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.recordInitial()
	_ = h.commitEdit(realCfg, []byte("pages:\n  - name: A\n"),
		gitCommitter{Email: "a@x", Name: "a"}, "edit 1")
	if err := h.rollbackLast(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	entries, _ := h.log(10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 commit after rollback, got %d", len(entries))
	}
}

func TestGitHistoryRestore(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	realCfg := tmp + "/glance.yml"
	_ = os.WriteFile(realCfg, []byte("pages: []\n"), 0o600)
	h, _ := openGitHistory(tmp+"/.glance-history", []string{realCfg})
	_ = h.recordInitial()
	_ = h.commitEdit(realCfg, []byte("pages:\n  - name: A\n"),
		gitCommitter{Email: "a@x", Name: "a"}, "edit A")
	_ = h.commitEdit(realCfg, []byte("pages:\n  - name: B\n"),
		gitCommitter{Email: "b@x", Name: "b"}, "edit B")

	entries, _ := h.log(10)
	// entries[2] is the initial commit.
	restored, err := h.restore(entries[2].SHA, gitCommitter{Email: "r@x", Name: "r"})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, _ := os.ReadFile(restored[0]); string(got) != "pages: []\n" {
		t.Fatalf("restore did not write initial contents, got %q", got)
	}
}

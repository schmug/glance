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

// TestCommitEditNoOpReturnsNil guards against the regression where a
// byte-identical save bubbles `git commit`'s "nothing to commit" exit-1 out
// to the user as an HTTP 500.
func TestCommitEditNoOpReturnsNil(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	realCfg := tmp + "/glance.yml"
	contents := []byte("pages: []\n")
	if err := os.WriteFile(realCfg, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := openGitHistory(tmp+"/.glance-history", []string{realCfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.recordInitial(); err != nil {
		t.Fatal(err)
	}
	if err := h.commitEdit(realCfg, contents,
		gitCommitter{Email: "a@x", Name: "a"}, "no-op"); err != nil {
		t.Fatalf("no-op save should succeed, got: %v", err)
	}
	entries, _ := h.log(10)
	if len(entries) != 1 {
		t.Fatalf("no-op save must not create a new commit; got %d entries", len(entries))
	}
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

// TestGitHistoryRestoreAtomicityWhenTargetPredatesFile guards against the bug
// where restore() overwrites earlier real files on disk before discovering that
// a later tracked path doesn't exist at the target sha — leaving live config in
// an inconsistent Frankenstein state.
func TestGitHistoryRestoreAtomicityWhenTargetPredatesFile(t *testing.T) {
	requireGit(t)
	tmp := t.TempDir()
	main := tmp + "/glance.yml"
	if err := os.WriteFile(main, []byte("pages: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Phase 1: open with only the main file and record the initial commit.
	// This sha won't contain a mirror for the include we add next.
	h1, err := openGitHistory(tmp+"/.glance-history", []string{main})
	if err != nil {
		t.Fatal(err)
	}
	if err := h1.recordInitial(); err != nil {
		t.Fatal(err)
	}
	initialEntries, err := h1.log(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(initialEntries) != 1 {
		t.Fatalf("want 1 initial commit, got %d", len(initialEntries))
	}
	initialSHA := initialEntries[0].SHA

	// Phase 2: user adds an include later, then re-opens history tracking both.
	include := tmp + "/include.yml"
	if err := os.WriteFile(include, []byte("widgets: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h2, err := openGitHistory(tmp+"/.glance-history", []string{main, include})
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.commitEdit(include, []byte("widgets: []\n"),
		gitCommitter{Email: "a@x", Name: "a"}, "add include"); err != nil {
		t.Fatal(err)
	}

	// Phase 3: user edits main to a distinctive value we can detect on disk.
	currentMain := []byte("pages:\n  - name: CURRENT\n")
	if err := os.WriteFile(main, currentMain, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h2.commitEdit(main, currentMain,
		gitCommitter{Email: "a@x", Name: "a"}, "edit main"); err != nil {
		t.Fatal(err)
	}

	// Phase 4: restore to initialSHA. include.yml's mirror doesn't exist there,
	// so the restore MUST fail.
	if _, err := h2.restore(initialSHA, gitCommitter{Email: "r@x", Name: "r"}); err == nil {
		t.Fatal("expected restore to fail when target sha predates an included file")
	}

	// Critical assertion: main.yml on disk must NOT have been overwritten
	// before the failure. A partial restore would have set it back to "pages: []\n".
	got, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(currentMain) {
		t.Fatalf("restore partially wrote main.yml: got %q, want %q", got, currentMain)
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

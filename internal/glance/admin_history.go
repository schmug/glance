package glance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type gitCommitter struct {
	Email, Name string
}

type gitHistoryEntry struct {
	SHA     string    `json:"SHA"`
	Time    time.Time `json:"Time"`
	Author  string    `json:"Author"`
	Email   string    `json:"Email"`
	Message string    `json:"Message"`
}

type gitHistory struct {
	dir        string   // absolute path to .glance-history/
	trackPaths []string // absolute real-world paths of config files
}

func openGitHistory(dir string, realFiles []string) (*gitHistory, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not on PATH: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	absFiles := make([]string, len(realFiles))
	for i, f := range realFiles {
		absFiles[i], _ = filepath.Abs(f)
	}

	h := &gitHistory{dir: abs, trackPaths: absFiles}
	if _, err := os.Stat(filepath.Join(abs, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, err
		}
		if _, err := h.git("init"); err != nil {
			return nil, fmt.Errorf("git init: %w", err)
		}
	}
	return h, nil
}

func (h *gitHistory) git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", h.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v (stderr: %s)", strings.Join(args, " "), err, errb.String())
	}
	return out.String(), nil
}

func (h *gitHistory) mirrorPath(real string) string {
	abs, _ := filepath.Abs(real)
	rel := strings.TrimPrefix(abs, string(os.PathSeparator))
	rel = strings.ReplaceAll(rel, string(os.PathSeparator), "__")
	return filepath.Join(h.dir, rel)
}

func (h *gitHistory) recordInitial() error {
	for _, p := range h.trackPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m := h.mirrorPath(p)
		if err := os.MkdirAll(filepath.Dir(m), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(m, data, 0o600); err != nil {
			return err
		}
	}
	if _, err := h.git("add", "--all"); err != nil {
		return err
	}
	if _, err := h.gitCommit(gitCommitter{Email: "admin@glance.local", Name: "Glance Admin"},
		"initial · "+time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return nil
}

func (h *gitHistory) gitCommit(c gitCommitter, message string) (string, error) {
	_, err := h.git(
		"-c", "user.email="+c.Email,
		"-c", "user.name="+c.Name,
		"commit", "-m", message,
	)
	return message, err
}

func (h *gitHistory) commitEdit(realPath string, newContents []byte, c gitCommitter, message string) error {
	m := h.mirrorPath(realPath)
	if err := os.MkdirAll(filepath.Dir(m), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(m, newContents, 0o600); err != nil {
		return err
	}
	if _, err := h.git("add", "--all"); err != nil {
		return err
	}
	_, err := h.gitCommit(c, message)
	return err
}

func (h *gitHistory) rollbackLast() error {
	_, err := h.git("reset", "--hard", "HEAD~1")
	return err
}

func (h *gitHistory) log(n int) ([]gitHistoryEntry, error) {
	fmtStr := "%H%x1f%cI%x1f%an%x1f%ae%x1f%s%x1e"
	out, err := h.git("log", fmt.Sprintf("-n%d", n), "--pretty=format:"+fmtStr)
	if err != nil {
		return nil, err
	}
	var entries []gitHistoryEntry
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		parts := strings.Split(rec, "\x1f")
		if len(parts) < 5 {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, parts[1])
		entries = append(entries, gitHistoryEntry{
			SHA: parts[0], Time: ts, Author: parts[2], Email: parts[3], Message: parts[4],
		})
	}
	return entries, nil
}

func (h *gitHistory) restore(sha string, c gitCommitter) ([]string, error) {
	for _, p := range h.trackPaths {
		m := h.mirrorPath(p)
		rel, err := filepath.Rel(h.dir, m)
		if err != nil {
			return nil, err
		}
		if _, err := h.git("checkout", sha, "--", rel); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return nil, err
		}
	}
	if _, err := h.git("add", "--all"); err != nil {
		return nil, err
	}
	shortSHA := sha
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}
	if _, err := h.gitCommit(c, fmt.Sprintf("restore %s · %s", shortSHA, time.Now().UTC().Format(time.RFC3339))); err != nil {
		return nil, err
	}
	return h.trackPaths, nil
}

func (h *gitHistory) diff(sha string) (string, error) {
	return h.git("show", "--format=", sha)
}

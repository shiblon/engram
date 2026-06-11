package engram

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectStorageRoot returns the checkout that owns project-local engram state.
// Ordinary repositories and independent clones use their own root. Linked git
// worktrees share the main worktree's root, so every branch checkout reads and
// writes the same .engram/mem.db instead of creating a worktree-local database.
func ProjectStorageRoot(root string) string {
	mainRoot, ok := linkedWorktreeMainRoot(root)
	if !ok {
		return root
	}
	return mainRoot
}

// gitConfigPath returns the config file for root's git repository. For linked
// worktrees the remote config lives in the common git dir, not the per-worktree
// git dir named by root/.git.
func gitConfigPath(root string) (string, bool) {
	gitDir, ok := resolveGitDir(root)
	if !ok {
		return "", false
	}
	return filepath.Join(commonGitDir(gitDir), "config"), true
}

func linkedWorktreeMainRoot(root string) (string, bool) {
	gitDir, gitFile, ok := resolveGitDirWithKind(root)
	if !ok || !gitFile {
		return "", false
	}
	if filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
		return "", false
	}
	commonDir := commonGitDir(gitDir)
	if filepath.Base(commonDir) != ".git" {
		return "", false
	}
	if info, err := os.Stat(commonDir); err != nil || !info.IsDir() {
		return "", false
	}
	return filepath.Dir(commonDir), true
}

func resolveGitDir(root string) (string, bool) {
	gitDir, _, ok := resolveGitDirWithKind(root)
	return gitDir, ok
}

func resolveGitDirWithKind(root string) (gitDir string, gitFile bool, ok bool) {
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", false, false
	}
	if info.IsDir() {
		return dotGit, false, true
	}
	gitDir, ok = parseGitDirFile(dotGit)
	return gitDir, true, ok
}

func parseGitDirFile(dotGit string) (string, bool) {
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	line := firstNonEmptyLine(string(data))
	dir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", false
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", false
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(dotGit), dir)
	}
	return filepath.Clean(dir), true
}

func commonGitDir(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	dir := firstNonEmptyLine(string(data))
	if dir == "" {
		return gitDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(gitDir, dir)
	}
	return filepath.Clean(dir)
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

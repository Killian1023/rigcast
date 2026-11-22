package syncfs

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

var excluded = map[string]bool{
	".git": true, ".venv": true, "venv": true, "__pycache__": true,
	"node_modules": true, ".pytest_cache": true, ".mypy_cache": true,
}

func WorkspaceFilter(root *os.Root) (Filter, error) {
	var patterns []gitignore.Pattern
	for _, name := range []string{".gitignore", ".rigcastignore"} {
		p, err := loadPatterns(root, name)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, p...)
	}
	matcher := gitignore.NewMatcher(patterns)
	return func(name string, dir bool) bool {
		// Skip a .git file too, as used by worktrees and submodules.
		if (dir && excluded[path.Base(name)]) || path.Base(name) == ".git" {
			return false
		}
		return !matcher.Match(strings.Split(name, "/"), dir)
	}, nil
}

func loadPatterns(root *os.Root, name string) ([]gitignore.Pattern, error) {
	f, err := root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	defer f.Close()
	var patterns []gitignore.Pattern
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return patterns, nil
}

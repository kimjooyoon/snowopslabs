// SPDX-License-Identifier: Apache-2.0

// Package display formats local paths for people without exposing account names.
package display

import (
	"os"
	"path/filepath"
	"strings"
)

// Path shortens a path for learner-facing text. Clone files are relative when
// the current directory is the clone root, and home paths use a tilde.
func Path(path, projectRoot, currentDir string) string {
	if path == "" {
		return path
	}
	clean := filepath.Clean(path)
	root := filepath.Clean(projectRoot)
	if root != "." && currentDir != "" && filepath.Clean(currentDir) == root {
		if rel, ok := relativeUnder(root, clean); ok {
			return rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, ok := relativeUnder(filepath.Clean(home), clean); ok {
			if rel == "." {
				return "~"
			}
			return filepath.Join("~", rel)
		}
	}
	return path
}

// Text shortens absolute clone and home paths embedded in learner-facing text.
func Text(text, projectRoot, stateDir, currentDir string) string {
	for _, root := range []string{projectRoot, stateDir} {
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		text = replacePathPrefix(text, filepath.Clean(root), projectRoot, currentDir)
	}
	return text
}

func replacePathPrefix(text, prefix, projectRoot, currentDir string) string {
	var out strings.Builder
	for {
		i := strings.Index(text, prefix)
		if i < 0 {
			out.WriteString(text)
			return out.String()
		}
		before, after := text[:i], text[i+len(prefix):]
		if !pathBoundary(after) {
			out.WriteString(before)
			out.WriteString(prefix)
			text = after
			continue
		}
		out.WriteString(before)
		replacement := Path(prefix, projectRoot, currentDir)
		if replacement == "." && strings.HasPrefix(after, string(filepath.Separator)) {
			replacement = ""
			after = after[1:]
		}
		out.WriteString(replacement)
		text = after
	}
}

func pathBoundary(suffix string) bool {
	return suffix == "" || suffix[0] == filepath.Separator || strings.ContainsRune(" \t\n\r'\"`)", rune(suffix[0]))
}

func relativeUnder(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

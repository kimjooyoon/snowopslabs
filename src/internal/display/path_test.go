// SPDX-License-Identifier: Apache-2.0

package display

import (
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "snowops")
	t.Setenv("HOME", home)
	tests := []struct {
		name string
		path string
		cwd  string
		want string
	}{
		{name: "empty path", path: "", cwd: root, want: ""},
		{name: "home becomes tilde", path: filepath.Join(home, ".snowops", "state", "lab"), cwd: filepath.Join(home, "other"), want: "~/.snowops/state/lab"},
		{name: "home directory becomes tilde", path: home, cwd: filepath.Join(home, "other"), want: "~"},
		{name: "clone path is relative at clone root", path: filepath.Join(root, "scenarios", "demo", "run.sh"), cwd: root, want: filepath.Join("scenarios", "demo", "run.sh")},
		{name: "clone path is home shortened elsewhere", path: filepath.Join(root, "scenarios", "demo", "run.sh"), cwd: filepath.Join(home, "other"), want: filepath.Join("~", "snowops", "scenarios", "demo", "run.sh")},
		{name: "outside home is unchanged", path: "/opt/snowops/scenarios/demo/run.sh", cwd: root, want: "/opt/snowops/scenarios/demo/run.sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Path(tt.path, root, tt.cwd); got != tt.want {
				t.Errorf("Path() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestText(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "snowops")
	state := filepath.Join(home, ".snowops", "state", "lab")
	t.Setenv("HOME", home)
	input := "bash " + filepath.Join(root, "scenarios", "demo", "restore.sh") + " and " + filepath.Join(state, "backups", "latest.json") + " plus " + root + "suffix"
	want := "bash scenarios/demo/restore.sh and ~/.snowops/state/lab/backups/latest.json plus " + root + "suffix"
	if got := Text(input, root, state, root); got != want {
		t.Fatalf("Text() = %q, want %q", got, want)
	}
	if got := Text("relative/path", "relative/root", state, root); got != "relative/path" {
		t.Errorf("Text() with a relative root = %q, want unchanged text", got)
	}
}

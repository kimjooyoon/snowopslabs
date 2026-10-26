// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"bytes"
	"strings"
	"testing"
)

// What a learner reads from the engine at the points where it carries on
// rather than failing: a scenario with checks, a scenario name used twice,
// and a scenario whose recorded app is gone.
func TestEngineMessages(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) string
		want  string
	}{
		{
			name: "up on a scenario with checks says how to wait for pods",
			setup: func(t *testing.T, root string) string {
				writeScenario(t, root, "with-checks", `name: with-checks
displayName: With Checks
description: one check, no components
category: testing
checks:
  - name: app-ready
    type: kubectl
    resource: deployment/demo
    namespace: demo
    jsonpath: "{.status.readyReplicas}"
    operator: ">="
    value: "1"
`)
				eng := NewEngine(root, "k3d.local", "k3d")
				var out bytes.Buffer
				eng.SetOutput(&out)
				if err := eng.Up("with-checks", &recordingExec{}, false); err != nil {
					t.Fatalf("Up: %v", err)
				}
				return out.String()
			},
			want: "Pods may still be starting - add --watch to wait for them:\n  labctl scenario verify with-checks --watch",
		},
		{
			name: "a second scenario with a taken name is skipped and named",
			setup: func(t *testing.T, root string) string {
				for _, dir := range []string{"first", "second"} {
					writeScenario(t, root, dir, "name: same-name\ndisplayName: Same\ndescription: x\ncategory: testing\n")
				}
				eng := NewEngine(root, "k3d.local", "k3d")
				var msgs []string
				for _, err := range eng.LoadErrors() {
					msgs = append(msgs, err.Error())
				}
				return strings.Join(msgs, "\n")
			},
			want: `scenario name "same-name" already provided by the repository - skipped`,
		},
		{
			name: "down carries on with the current app when the recorded one is gone",
			setup: func(t *testing.T, root string) string {
				writeScenario(t, root, "orphaned", "name: orphaned\ndisplayName: Orphaned\ndescription: x\ncategory: testing\n")
				eng := NewEngine(root, "k3d.local", "k3d")
				eng.Workload.Name = "removed-app"
				if err := eng.markActive("orphaned", nil); err != nil {
					t.Fatalf("markActive: %v", err)
				}
				eng.Workload.Name = "go-api"
				var out bytes.Buffer
				eng.SetOutput(&out)
				if err := eng.Down("orphaned", &recordingExec{}); err != nil {
					t.Fatalf("Down: %v", err)
				}
				return out.String()
			},
			want: "- continuing against go-api.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.setup(t, t.TempDir())
			if !strings.Contains(got, tt.want) {
				t.Errorf("output does not contain %q:\n%s", tt.want, got)
			}
		})
	}
}

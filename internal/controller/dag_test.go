package controller

import (
	"sort"
	"testing"
)

func TestDAG_Validate(t *testing.T) {
	tests := []struct {
		name    string
		tasks   map[string][]string
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid linear DAG",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"b"},
			},
			wantErr: false,
		},
		{
			name: "valid diamond DAG",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"a"},
				"d": {"b", "c"},
			},
			wantErr: false,
		},
		{
			name: "single task no deps",
			tasks: map[string][]string{
				"only": {},
			},
			wantErr: false,
		},
		{
			name: "cycle between two tasks",
			tasks: map[string][]string{
				"a": {"b"},
				"b": {"a"},
			},
			wantErr: true,
			errMsg:  "cycle detected",
		},
		{
			name: "self-referencing task",
			tasks: map[string][]string{
				"a": {"a"},
			},
			wantErr: true,
			errMsg:  "cycle detected",
		},
		{
			name: "unknown dependency reference",
			tasks: map[string][]string{
				"a": {},
				"b": {"c"},
			},
			wantErr: true,
			errMsg:  "unknown task",
		},
		{
			name: "three-node cycle",
			tasks: map[string][]string{
				"a": {"c"},
				"b": {"a"},
				"c": {"b"},
			},
			wantErr: true,
			errMsg:  "cycle detected",
		},
		{
			name:    "empty DAG",
			tasks:   map[string][]string{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dag := NewDAG(tt.tasks)
			err := dag.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestDAG_TopologicalSort(t *testing.T) {
	tests := []struct {
		name    string
		tasks   map[string][]string
		wantErr bool
	}{
		{
			name: "linear chain",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"b"},
			},
			wantErr: false,
		},
		{
			name: "diamond",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"a"},
				"d": {"b", "c"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dag := NewDAG(tt.tasks)
			sorted, err := dag.TopologicalSort()
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(sorted) != len(tt.tasks) {
				t.Errorf("expected %d tasks in sorted output, got %d", len(tt.tasks), len(sorted))
			}

			// Verify ordering: each task appears after its dependencies.
			pos := make(map[string]int)
			for i, name := range sorted {
				pos[name] = i
			}
			for name, deps := range tt.tasks {
				for _, dep := range deps {
					if pos[dep] >= pos[name] {
						t.Errorf("task %q (pos %d) should come after dependency %q (pos %d)", name, pos[name], dep, pos[dep])
					}
				}
			}
		})
	}
}

func TestDAG_RootNodes(t *testing.T) {
	tests := []struct {
		name     string
		tasks    map[string][]string
		expected []string
	}{
		{
			name: "single root",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
			},
			expected: []string{"a"},
		},
		{
			name: "multiple roots",
			tasks: map[string][]string{
				"a": {},
				"b": {},
				"c": {"a", "b"},
			},
			expected: []string{"a", "b"},
		},
		{
			name: "all roots",
			tasks: map[string][]string{
				"a": {},
				"b": {},
			},
			expected: []string{"a", "b"},
		},
		{
			name:     "empty",
			tasks:    map[string][]string{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dag := NewDAG(tt.tasks)
			roots := dag.RootNodes()
			sort.Strings(roots)
			sort.Strings(tt.expected)
			if len(roots) != len(tt.expected) {
				t.Errorf("expected roots %v, got %v", tt.expected, roots)
				return
			}
			for i := range roots {
				if roots[i] != tt.expected[i] {
					t.Errorf("expected roots %v, got %v", tt.expected, roots)
					break
				}
			}
		})
	}
}

func TestDAG_RunnableTasks(t *testing.T) {
	tests := []struct {
		name      string
		tasks     map[string][]string
		completed map[string]bool
		expected  []string
	}{
		{
			name: "nothing completed, roots are runnable",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"a"},
			},
			completed: map[string]bool{},
			expected:  []string{"a"},
		},
		{
			name: "root completed, dependents are runnable",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"a"},
			},
			completed: map[string]bool{"a": true},
			expected:  []string{"b", "c"},
		},
		{
			name: "diamond - partial deps met",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"a"},
				"d": {"b", "c"},
			},
			completed: map[string]bool{"a": true, "b": true},
			expected:  []string{"c"},
		},
		{
			name: "diamond - all deps met for final",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
				"c": {"a"},
				"d": {"b", "c"},
			},
			completed: map[string]bool{"a": true, "b": true, "c": true},
			expected:  []string{"d"},
		},
		{
			name: "all completed",
			tasks: map[string][]string{
				"a": {},
				"b": {"a"},
			},
			completed: map[string]bool{"a": true, "b": true},
			expected:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dag := NewDAG(tt.tasks)
			runnable := dag.RunnableTasks(tt.completed)
			sort.Strings(runnable)
			sort.Strings(tt.expected)
			if len(runnable) != len(tt.expected) {
				t.Errorf("expected runnable %v, got %v", tt.expected, runnable)
				return
			}
			for i := range runnable {
				if runnable[i] != tt.expected[i] {
					t.Errorf("expected runnable %v, got %v", tt.expected, runnable)
					break
				}
			}
		})
	}
}

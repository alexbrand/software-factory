package controller

import "fmt"

// DAG represents a directed acyclic graph of workflow tasks.
type DAG struct {
	// Nodes maps task name to its dependencies.
	Nodes map[string][]string
}

// NewDAG creates a DAG from a list of task names and their dependencies.
func NewDAG(tasks map[string][]string) *DAG {
	return &DAG{Nodes: tasks}
}

// Validate checks that all dependency references exist and there are no cycles.
func (d *DAG) Validate() error {
	// Check all dependency references exist.
	for name, deps := range d.Nodes {
		for _, dep := range deps {
			if _, ok := d.Nodes[dep]; !ok {
				return fmt.Errorf("task %q depends on unknown task %q", name, dep)
			}
		}
	}

	// Check for cycles using topological sort (Kahn's algorithm).
	_, err := d.TopologicalSort()
	return err
}

// TopologicalSort returns tasks in topological order or an error if cycles exist.
func (d *DAG) TopologicalSort() ([]string, error) {
	// Build adjacency list (dep -> dependents) and compute in-degrees.
	inDegree := make(map[string]int, len(d.Nodes))
	adj := make(map[string][]string, len(d.Nodes))
	for name, deps := range d.Nodes {
		inDegree[name] = len(deps)
		for _, dep := range deps {
			adj[dep] = append(adj[dep], name)
		}
	}

	// Start with nodes that have no dependencies.
	var queue []string
	for name, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, name)
		}
	}

	var sorted []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		sorted = append(sorted, node)

		for _, dependent := range adj[node] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(sorted) != len(d.Nodes) {
		return nil, fmt.Errorf("cycle detected in task DAG")
	}

	return sorted, nil
}

// RootNodes returns tasks with no dependencies.
func (d *DAG) RootNodes() []string {
	var roots []string
	for name, deps := range d.Nodes {
		if len(deps) == 0 {
			roots = append(roots, name)
		}
	}
	return roots
}

// RunnableTasks returns tasks whose dependencies are all in the completed set.
func (d *DAG) RunnableTasks(completed map[string]bool) []string {
	var runnable []string
	for name, deps := range d.Nodes {
		if completed[name] {
			continue
		}
		allMet := true
		for _, dep := range deps {
			if !completed[dep] {
				allMet = false
				break
			}
		}
		if allMet {
			runnable = append(runnable, name)
		}
	}
	return runnable
}

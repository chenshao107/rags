package config

import (
	"testing"
)

func TestCollectionNameForPathDistinctPaths(t *testing.T) {
	a := CollectionNameForPath("/disk2/foo/Task任务", "Task任务")
	b := CollectionNameForPath("/disk2/bar/Task任务", "Task任务")
	if a == b {
		t.Fatalf("expected different collections for same basename: %q vs %q", a, b)
	}
	if !stringsHasPrefix(a, "rags_task_") {
		t.Fatalf("unexpected format: %q", a)
	}
}

func TestCollectionNameForPathStable(t *testing.T) {
	path := "/disk2/foo/Task任务"
	a := CollectionNameForPath(path, "Task任务")
	b := CollectionNameForPath(path, "Task任务")
	if a != b {
		t.Fatalf("expected stable name: %q vs %q", a, b)
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

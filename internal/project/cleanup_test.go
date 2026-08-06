package project

import (
	"testing"

	"github.com/chenshao/rags/internal/config"
)

func TestNeedsFullReindexReset(t *testing.T) {
	empty := config.ProjectState{Files: map[string]string{}}
	withFiles := config.ProjectState{Files: map[string]string{"a.txt": "hash"}}

	if !NeedsFullReindexReset(empty, 10, 10) {
		t.Fatal("expected full reset when state empty and all files new")
	}
	if NeedsFullReindexReset(withFiles, 10, 10) {
		t.Fatal("unexpected reset when state has files")
	}
	if NeedsFullReindexReset(empty, 10, 5) {
		t.Fatal("unexpected reset when only partial index")
	}
}

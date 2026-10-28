package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingDockerSettingsHintsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSettingsFromDir(dir)
	got := MissingDockerSettings(dir, err)
	var hint *DockerHostUpdateError
	if !errors.As(got, &hint) {
		t.Fatalf("expected docker update hint, got %v", got)
	}
	if !errors.Is(got, os.ErrNotExist) {
		t.Fatal("hint should unwrap to the missing file")
	}
	msg := got.Error()
	if !strings.Contains(msg, "docker && ./update.sh") {
		t.Fatalf("hint missing update command:\n%s", msg)
	}
	if !strings.Contains(msg, filepath.Join(dir, SettingsFile)) {
		t.Fatalf("hint missing settings path:\n%s", msg)
	}
}

func TestMissingDockerSettingsKeepsOtherErrors(t *testing.T) {
	other := errors.New("parse settings: bad yaml")
	if got := MissingDockerSettings(DockerSettingsDir, other); got != other {
		t.Fatalf("got %v", got)
	}
	if got := MissingDockerSettings(DockerSettingsDir, nil); got != nil {
		t.Fatalf("got %v", got)
	}
}

package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	ModePrivate = "private"
	ModePublic  = "public"

	DeploySystemd = "systemd"
	DeployDocker  = "docker"

	PublicRepoRoot = "/srv/rags"
	PublicRunDir   = "/run/rags"
	RootHome       = "/root"

	DockerDocsRoot     = "/data/docs"
	DockerSettingsDir  = "/etc/rags"
)

var publicNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// PublicMeta holds metadata for a shared public repository.
type PublicMeta struct {
	RepoRoot    string `yaml:"repo_root"`
	DocsRoot    string `yaml:"docs_root"`
	Endpoint    string `yaml:"endpoint"`
	Service     string `yaml:"service,omitempty"`
	Collection  string `yaml:"collection"`
	SettingsDir string `yaml:"settings_dir"`
	Deploy      string `yaml:"deploy,omitempty"` // systemd or docker
}

// IsPublic reports whether the project is a shared public repository.
func (c ProjectConfig) IsPublic() bool {
	return c.Mode == ModePublic
}

// ValidatePublicName checks whether a public repo name is safe for paths and systemd.
func ValidatePublicName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("public repo name is empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("public repo name too long (max 64): %q", name)
	}
	if !publicNamePattern.MatchString(name) {
		return fmt.Errorf("invalid public repo name %q: use [a-z0-9][a-z0-9_-]*", name)
	}
	return nil
}

// PublicRepoPath returns /srv/rags/<name>.
func PublicRepoPath(name string) string {
	return filepath.Join(PublicRepoRoot, name)
}

// PublicSocketPath returns /run/rags/<name>.sock.
func PublicSocketPath(name string) string {
	return filepath.Join(PublicRunDir, name+".sock")
}

// PublicEndpoint returns unix:///run/rags/<name>.sock.
func PublicEndpoint(name string) string {
	return "unix://" + PublicSocketPath(name)
}

// PublicServiceName returns rags-public@<name>.
func PublicServiceName(name string) string {
	return "rags-public@" + name
}

// BuildPublicMeta constructs public metadata for a repository.
func BuildPublicMeta(name, repoRoot, docsRoot, collection, settingsDir, endpoint, deploy string) PublicMeta {
	meta := PublicMeta{
		RepoRoot:    repoRoot,
		DocsRoot:    docsRoot,
		Endpoint:    endpoint,
		Collection:  collection,
		SettingsDir: settingsDir,
		Deploy:      deploy,
	}
	if deploy == DeploySystemd {
		meta.Service = PublicServiceName(name)
	}
	return meta
}

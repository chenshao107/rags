package config

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// SanitizeName converts a project name into a safe collection suffix.
func SanitizeName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	result := b.String()
	if len(result) < 3 {
		h := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
		result = h[:8]
	}
	return result
}

// CollectionName returns the default Qdrant collection for a project name.
func CollectionName(name string) string {
	return "rags_" + SanitizeName(name)
}

// DefaultPublicName derives a public repo name from a source path.
func DefaultPublicName(sourcePath string) string {
	base := filepath.Base(sourcePath)
	base = strings.ToLower(base)
	base = strings.ReplaceAll(base, " ", "-")
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-_")
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	if result == "" {
		h := fmt.Sprintf("%x", sha256.Sum256([]byte(sourcePath)))
		result = "repo-" + h[:8]
	}
	return result
}

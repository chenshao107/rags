package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ParseEndpoint classifies a public repo endpoint.
func ParseEndpoint(endpoint string) (scheme string, err error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("empty endpoint")
	}
	if strings.HasPrefix(endpoint, "unix://") {
		return "unix", nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("endpoint missing host: %s", endpoint)
	}
	return u.Scheme, nil
}

// UnixSocketPath extracts the filesystem path from a unix:// endpoint.
func UnixSocketPath(endpoint string) (string, error) {
	if !strings.HasPrefix(endpoint, "unix://") {
		return "", fmt.Errorf("not a unix endpoint: %s", endpoint)
	}
	path := strings.TrimPrefix(endpoint, "unix://")
	if path == "" {
		return "", fmt.Errorf("empty unix socket path")
	}
	return path, nil
}

// HTTPEndpoint builds http://host:port.
func HTTPEndpoint(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

// ServerListenAddr returns the network and address for net.Listen.
func ServerListenAddr(endpoint string) (network string, addr string, err error) {
	if strings.HasPrefix(endpoint, "unix://") {
		path, err := UnixSocketPath(endpoint)
		if err != nil {
			return "", "", err
		}
		return "unix", path, nil
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("parse endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}

	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", "", fmt.Errorf("invalid port %q: %w", port, err)
	}
	// Bind all interfaces inside container/host service.
	return "tcp", ":" + port, nil
}

// ClientSearchURL returns the full URL for POST /search.
func ClientSearchURL(endpoint string) (string, error) {
	if strings.HasPrefix(endpoint, "unix://") {
		return "http://rags/search", nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/search"
	u.RawPath = ""
	u.RawQuery = ""
	return u.String(), nil
}

// IsUnixEndpoint reports whether endpoint uses a unix socket.
func IsUnixEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "unix://")
}

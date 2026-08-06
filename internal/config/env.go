package config

import (
	"strconv"
	"strings"
)

func parseEnvInt(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

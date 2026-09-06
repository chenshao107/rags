package systemd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const unitTemplate = `[Unit]
Description=Rags public search server (%%i)
After=network-online.target

[Service]
Type=simple
User=root
Group=root
ExecStart=%s serve --name %%i
RuntimeDirectory=rags
RuntimeDirectoryMode=0755
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
`

// InstallPublicService writes and enables a systemd template instance for a public repo.
func InstallPublicService(name string) error {
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve rags binary: %w", err)
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return fmt.Errorf("resolve rags binary path: %w", err)
	}

	unitPath := "/etc/systemd/system/rags-public@.service"
	content := fmt.Sprintf(unitTemplate, shellQuote(bin))
	if err := os.WriteFile(unitPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}

	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "--now", "rags-public@"+name+".service"); err != nil {
		return err
	}
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func shellQuote(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\"'$\\") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

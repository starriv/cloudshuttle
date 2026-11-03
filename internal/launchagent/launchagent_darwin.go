package launchagent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// Register makes launchd start the agent at wakes and at login, replacing
// what it did before; no wakes removes the agent. An agent already loaded
// as it would be is left as it is, so that registering the same schedules
// again does not start it.
func (a Agent) Register(wakes []domain.Wake) error {
	path, err := a.path()
	if err != nil {
		return err
	}
	if len(wakes) == 0 {
		a.bootout()
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	want := a.plist(wakes)
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, want) && a.Loaded() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// launchd reads a job's definition as it loads it: a changed one is
	// unloaded first.
	a.bootout()
	if out, err := exec.Command("launchctl", "bootstrap", a.domain(), path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if !a.Loaded() {
		// The user turned it off in System Settings > General > Login
		// Items: launchd keeps it but does not load it.
		return ErrDisabled
	}
	return nil
}

// Loaded reports whether launchd has the agent.
func (a Agent) Loaded() bool {
	return exec.Command("launchctl", "print", a.domain()+"/"+a.Label).Run() == nil
}

func (a Agent) bootout() {
	_ = exec.Command("launchctl", "bootout", a.domain()+"/"+a.Label).Run()
}

func (a Agent) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (a Agent) path() (string, error) {
	dir := a.Dir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, "Library", "LaunchAgents")
	}
	return filepath.Join(dir, a.Label+".plist"), nil
}

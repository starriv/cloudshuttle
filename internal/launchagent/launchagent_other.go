//go:build !darwin

package launchagent

import (
	"errors"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// Register fails: the system has no scheduler the app registers with.
func (a Agent) Register(wakes []domain.Wake) error { return errors.ErrUnsupported }

// Loaded reports false.
func (a Agent) Loaded() bool { return false }

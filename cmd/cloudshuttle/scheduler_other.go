//go:build !darwin

package main

import (
	"errors"

	"github.com/starriv/cloudshuttle/internal/service"
)

// newRegistrar is nil: the system starts no scheduler for the app here,
// and the window runs the schedules while the app is open.
func newRegistrar() service.Registrar { return nil }

// wakeApp is not needed where no scheduler runs apart from the window.
func wakeApp() {}

func openBackgroundSettings() error { return errors.ErrUnsupported }

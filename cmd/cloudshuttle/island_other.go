//go:build !darwin

package main

import (
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/service"
)

// notchIsland is macOS's alone: elsewhere the tray and notifications tell
// how runs go.
type notchIsland struct{}

func newIsland() *notchIsland { return &notchIsland{} }

func (*notchIsland) SetRuns([]service.Active) {}

func (*notchIsland) Finished(domain.Run) {}

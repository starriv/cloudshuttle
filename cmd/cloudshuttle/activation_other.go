//go:build !darwin

package main

// showDockIconWithoutActivation does nothing where there is no Dock: the app
// keeps its taskbar entry whatever the activation policy would say.
func showDockIconWithoutActivation() {}

//go:build darwin

package main

import "github.com/ebitengine/purego/objc"

// nsApplicationActivationPolicyRegular is NSApplicationActivationPolicyRegular:
// an app with a Dock icon and a menu bar.
const nsApplicationActivationPolicyRegular = 0

// showDockIconWithoutActivation gives the app a Dock icon without bringing it
// to the front. mygo's SetActivationPolicy activates the app after making it
// regular, as a person opening the app wants; a launch the system makes in the
// background, to tell a failure or to finish an update, must not take the
// foreground from what the person is doing.
func showDockIconWithoutActivation() {
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	objc.Send[bool](app, objc.RegisterName("setActivationPolicy:"), nsApplicationActivationPolicyRegular)
}

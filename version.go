package proxyguard

import (
	"fmt"
	"runtime/debug"
)

// version is the current version
const version = "0.4.0"

// versionReleased sets whether or not the current version is released yet
const versionReleased = false

// Version() gets the version in the following order:
// - Gets a release version if it detects it is a release
// - Gets the commit using debug info
// - Returns a default
func Version() string {
	if versionReleased {
		return version
	}
	if dbg, ok := debug.ReadBuildInfo(); ok {
		for _, s := range dbg.Settings {
			if s.Key == "vcs.revision" {
				return fmt.Sprintf("(devel) %s", s.Value)
			}
		}
	}
	return "(unknown)"
}

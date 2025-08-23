package httpserver

import "sync/atomic"

var draining atomic.Bool
var drainingEnabled atomic.Bool

func EnableDrainFlag(on bool) { drainingEnabled.Store(on) }
func SetDraining(on bool) {
	if drainingEnabled.Load() {
		draining.Store(on)
	}
}
func IsDraining() bool { return drainingEnabled.Load() && draining.Load() }

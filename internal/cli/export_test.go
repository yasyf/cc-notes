package cli

// SetMountpointLiveForTest overrides the mountpoint-liveness seam and returns a
// restore func, so external --stop tests can drive the holder-contacting
// teardown path without a real kernel mount (forbidden in tests).
func SetMountpointLiveForTest(fn func(string) bool) (restore func()) {
	old := mountpointLive
	mountpointLive = fn
	return func() { mountpointLive = old }
}

// SetHostGOOSForTest overrides the platform-gate seam and returns a restore
// func, so external detached-mount tests can drive the darwin-only serveDetached
// path on any CI platform (Linux CI would otherwise hit the !darwin fail-fast
// before the fake holder engages).
func SetHostGOOSForTest(goos string) (restore func()) {
	old := hostGOOS
	hostGOOS = goos
	return func() { hostGOOS = old }
}

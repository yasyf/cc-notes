package cli

// SetMountpointLiveForTest overrides the mountpoint-liveness seam and returns a
// restore func, so external --stop tests can drive the holder-contacting
// teardown path without a real kernel mount (forbidden in tests).
func SetMountpointLiveForTest(fn func(string) bool) (restore func()) {
	old := mountpointLive
	mountpointLive = fn
	return func() { mountpointLive = old }
}

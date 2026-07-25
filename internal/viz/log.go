package viz

// Logger is the operational log sink for the viz layer: slow graph builds and
// server-fault responses. It is *log.Logger narrowed to the one method viz
// calls, so the CLI hands over its stderr logger and a test hands over a
// buffer.
type Logger interface {
	Printf(format string, v ...any)
}

// nopLogger discards every line. NewBuilder installs it so a Builder driven
// outside the server (tests, embedders) logs nothing without any call site
// having to check.
type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

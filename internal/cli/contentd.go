package cli

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/fusekit/content"
)

// contentdLabel is the LaunchAgent label for cc-notes' content server.
const contentdLabel = "com.yasyf.cc-notes.contentd"

// newContentdCmd is the hidden entry point for cc-notes' content server, run by
// the com.yasyf.cc-notes.contentd LaunchAgent. It serves the store→tree renderer
// on the bridge data socket the shared fusekit holder dials for every cc-notes
// tree mount; the LaunchAgent's KeepAlive keeps it up. It blocks until its
// context is canceled (the CLI wires SIGINT/SIGTERM).
func newContentdCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "contentd",
		Short:  "Run cc-notes' content server for the shared fusekit holder",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := os.MkdirAll(fusekitSpoolDir(), 0o700); err != nil {
				return fmt.Errorf("create spool dir: %w", err)
			}
			// Stamp this binary's identity beside the socket so ensureContentd can
			// detect a brew upgrade that left the old renderer serving (see
			// contentdStampFresh).
			if err := writeContentdStamp(); err != nil {
				return err
			}
			return fusefs.ServeContent(cmd.Context(), contentSocketPath())
		},
	}
}

// contentd lifecycle seams: tests drive the cold-start / staleness-recycle /
// await paths without a real bridge server or launchctl.
var (
	// contentdAvailable reports whether contentd is answering socket.
	contentdAvailable = func(socket string) bool { return content.NewBridgeClient(socket).Available() }
	// contentdInstall (re)installs and loads the contentd LaunchAgent.
	contentdInstall = installContentdAgent
	// contentdReadyTimeout bounds the post-install wait for contentd to bind.
	contentdReadyTimeout = 5 * time.Second
	// contentdPollInterval is the await dial-poll cadence.
	contentdPollInterval = 50 * time.Millisecond
)

// ensureContentd makes cc-notes' content server available before a detached
// mount, so the shared holder can dial its socket. It is a no-op on a binary
// that cannot host fuse (a pure build serves no content — the holder's mount
// then fails loudly with content-unavailable); otherwise it defers to
// startContentd.
func ensureContentd(_ *cobra.Command) error {
	if !fusefs.Hostable {
		return nil
	}
	return startContentd()
}

// startContentd makes THIS installed binary's contentd available before a
// detached mount. It (re)installs the LaunchAgent on a cold socket OR when the
// serving contentd's boot stamp does not match the installed binary (a brew
// upgrade that swapped the renderer), then AWAITS the socket so the first
// AddMount never races contentd's bind.
//
// contentd MUST be up BEFORE the holder serves a cc-notes mount: the holder
// re-serves a journaled tree mount by dialing this socket at replay time, and
// three failed replays drop the row for good. A KeepAlive LaunchAgent — not a
// CLI-lazy start bound to a single `mount` invocation — is what keeps the socket
// answering across holder restarts and reboots.
func startContentd() error {
	if err := os.MkdirAll(fusekitSpoolDir(), 0o700); err != nil {
		return fmt.Errorf("create spool dir: %w", err)
	}
	if contentdAvailable(contentSocketPath()) {
		fresh, err := contentdStampFresh()
		if err != nil {
			return err
		}
		if fresh {
			return nil
		}
	}
	if err := contentdInstall(); err != nil {
		return err
	}
	return awaitContentd(contentSocketPath())
}

// awaitContentd blocks until contentd answers socket or the ready timeout
// elapses, so the first AddMount never precedes contentd's bind (a cold
// launchctl bootstrap returns before the agent's socket exists). A timeout is a
// crisp error naming the LaunchAgent and its log.
func awaitContentd(socket string) error {
	deadline := time.Now().Add(contentdReadyTimeout)
	for {
		if contentdAvailable(socket) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cc-notes' content server (%s LaunchAgent) did not bind %s within %s; check %s", contentdLabel, socket, contentdReadyTimeout, contentdLogPath())
		}
		time.Sleep(contentdPollInterval)
	}
}

// binaryStamp identifies exe for the contentd staleness check: the injected
// build version when present, else a digest of the executable's size and mtime
// (ditto preserves mtime across a cask upgrade, but the size almost always
// changes) so a brew upgrade that swaps the binary is detected even on a dev
// build with no version.
func binaryStamp(exe string) (string, error) {
	if v := version.Version; v != "dev" {
		return "v=" + v, nil
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", exe, err)
	}
	return fmt.Sprintf("size=%d mtime=%d", fi.Size(), fi.ModTime().UnixNano()), nil
}

// contentdStampPath is where contentd stamps its binary identity at startup
// (~/.fusekit/spool/cc-notes/contentd.stamp), beside its socket.
func contentdStampPath() string {
	return filepath.Join(fusekitSpoolDir(), "contentd.stamp")
}

// writeContentdStamp records the running binary's identity beside the socket so
// a later ensureContentd can tell whether the serving contentd is still this
// installed binary (see contentdStampFresh).
func writeContentdStamp() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	stamp, err := binaryStamp(exe)
	if err != nil {
		return err
	}
	if err := os.WriteFile(contentdStampPath(), []byte(stamp), 0o644); err != nil {
		return fmt.Errorf("write contentd stamp: %w", err)
	}
	return nil
}

// contentdStampFresh reports whether the contentd currently serving the socket
// is THIS installed binary, comparing the stamp it wrote at startup against the
// running binary's identity. A missing stamp or a mismatch is stale (⇒ recycle):
// an old contentd from before a brew upgrade keeps serving the old renderer until
// its LaunchAgent is rebooted.
func contentdStampFresh() (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("resolve executable: %w", err)
	}
	want, err := binaryStamp(exe)
	if err != nil {
		return false, err
	}
	got, err := os.ReadFile(contentdStampPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read contentd stamp: %w", err)
	}
	return strings.TrimSpace(string(got)) == want, nil
}

// installContentdAgent writes the contentd LaunchAgent plist and loads it. The
// plist runs THIS binary's `contentd` subcommand under KeepAlive; the log lands
// beside cc-notes' state. It REFUSES to install a `go test` binary as the
// LaunchAgent — a KeepAlive agent that re-execs a test binary would re-run the
// suite forever (this repo's fork-storm class); the refusal fires before any
// filesystem or launchctl side effect, so a developer running
// `go test -tags fuse ./internal/cli/...` by hand is protected structurally.
func installContentdAgent() error {
	if hostGOOS != "darwin" {
		return fmt.Errorf("cc-notes' content server installs a launchd LaunchAgent and runs only on macOS; on %s use `cc-notes mount --foreground` to serve in-process", hostGOOS)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if isTestBinary(exe) {
		return fmt.Errorf("refusing to install the contentd LaunchAgent from a test binary (%s): a KeepAlive agent re-execing a `go test` binary re-runs the suite (fork storm)", exe)
	}
	if err := os.MkdirAll(filepath.Dir(contentdPlistPath()), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := writeContentdPlist(exe); err != nil {
		return err
	}
	return launchctlLoad(contentdPlistPath(), contentdLabel)
}

// installBinaryFamily is the set of basenames cc-notes ships as: the real
// installed binary (cc-notes), its `ccn` shorthand symlink (install.sh links
// ccn -> cc-notes; os.Executable() reports the symlink path UNRESOLVED on macOS,
// so `ccn mount` presents basename "ccn"), and a locally-built fuse variant
// (cc-notes_fuse). Anything else is not the shipped binary.
var installBinaryFamily = map[string]bool{"cc-notes": true, "ccn": true, "cc-notes_fuse": true}

// isTestBinary reports whether exe must NOT be installed as the contentd
// KeepAlive LaunchAgent — one that re-execs a `go test` binary re-runs the suite
// forever (this repo's fork-storm class). It guards the developer-mistake class,
// NOT a determined adversary on their own machine: it refuses any basename
// outside the shipped family (a `<pkg>.test` binary, a `go test -c -o /tmp/runner`
// build), and any executable under the go-build cache or a temp dir (a
// `go test -c -o "$TMPDIR/cc-notes"` build that keeps the shipped name).
func isTestBinary(exe string) bool {
	if !installBinaryFamily[filepath.Base(exe)] {
		return true
	}
	if strings.Contains(exe, "/go-build") {
		return true
	}
	if tmp := os.TempDir(); tmp != "" && strings.HasPrefix(exe, filepath.Clean(tmp)+string(os.PathSeparator)) {
		return true
	}
	return false
}

// contentdPlistPath is the LaunchAgent plist path
// (~/Library/LaunchAgents/com.yasyf.cc-notes.contentd.plist).
func contentdPlistPath() string {
	return filepath.Join(mustHome(), "Library", "LaunchAgents", contentdLabel+".plist")
}

// contentdLogPath is where the LaunchAgent-run contentd's stdout and stderr land
// (~/.cc-notes/contentd.log).
func contentdLogPath() string {
	return filepath.Join(stateDir(), "contentd.log")
}

// writeContentdPlist writes the KeepAlive LaunchAgent plist that runs exe's
// `contentd` subcommand. The dynamic paths are XML-escaped so a home dir with a
// metacharacter cannot corrupt the plist.
func writeContentdPlist(exe string) error {
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + contentdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(exe) + `</string>
		<string>contentd</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(contentdLogPath()) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(contentdLogPath()) + `</string>
</dict>
</plist>
`
	if err := os.WriteFile(contentdPlistPath(), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", contentdPlistPath(), err)
	}
	return nil
}

// launchctlLoad (re)loads the LaunchAgent at plistPath into the user's GUI
// domain, picking up a changed plist. It is a seam so tests never shell out to
// launchctl. Booting out a not-loaded agent is a benign error (ignored); a
// bootstrap failure is surfaced.
var launchctlLoad = func(plistPath, label string) error {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+label).Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap %s: %w: %s", label, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// xmlEscape escapes s for inclusion in a plist string element.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

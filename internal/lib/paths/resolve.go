package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// probe is the view of the environment that layout selection needs.
type probe interface {
	GOOS() string
	Getenv(key string) string
	Exists(path string) bool
}

type osProbe struct{}

func (osProbe) GOOS() string             { return runtime.GOOS }
func (osProbe) Getenv(key string) string { return os.Getenv(key) }
func (osProbe) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func validateApp(app string) error {
	if app == "" || app == "." || app == ".." || strings.ContainsAny(app, "/\\\x00") {
		return fmt.Errorf("paths: invalid app name %q", app)
	}
	return nil
}

// platform is a runtime environment with its own directory layout.
type platform int

const (
	platformUnknown platform = iota
	platformWindows
	platformLinux
	platformOpenWrt
)

// detect picks the platform. OpenWrt is a Linux variant recognised by /etc/openwrt_release.
func detect(pr probe) platform {
	switch pr.GOOS() {
	case "windows":
		return platformWindows
	case "linux":
		if pr.Exists("/etc/openwrt_release") {
			return platformOpenWrt
		}
		return platformLinux
	}
	return platformUnknown
}

// resolve maps a platform to its layout. To support a new platform, add a constant, a detect rule
// and a case here; nothing else in the package changes.
func resolve(app string, pr probe) (Paths, error) {
	if err := validateApp(app); err != nil {
		return Paths{}, err
	}
	switch detect(pr) {
	case platformWindows:
		return windowsLayout(app, pr)
	case platformLinux:
		return linuxLayout(app), nil
	case platformOpenWrt:
		// Not implemented yet. Falling back to the Linux layout would be wrong: /var and /run are
		// RAM-backed on OpenWrt, so data and secrets would vanish on reboot. Fail loudly instead.
		return Paths{}, fmt.Errorf("%w: OpenWrt layout not implemented", ErrUnsupported)
	}
	return Paths{}, ErrUnsupported
}

func windowsLayout(app string, pr probe) (Paths, error) {
	base := pr.Getenv("ProgramData")
	if base == "" {
		return Paths{}, errors.New("paths: %ProgramData% is not set")
	}
	return FromRoot(filepath.Join(base, app)), nil
}

func linuxLayout(app string) Paths {
	return Paths{
		Config:  "/etc/" + app,
		Data:    "/var/lib/" + app,
		Logs:    "/var/log/" + app,
		Secrets: "/etc/" + app + "/secrets",
		Run:     "/run/" + app,
	}
}

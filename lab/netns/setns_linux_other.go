//go:build linux && !amd64 && !arm64 && !mipsle

package netns

// sysSetns is unknown on this architecture; Do returns errors.ErrUnsupported.
const sysSetns = 0

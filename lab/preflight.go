//go:build linux

package lab

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Preflight reports what the host lacks: root, /dev/net/tun, the ip, nft and curl commands, the
// nfnetlink_queue and nft_queue kernel modules, and the pinned sing-box. It returns one error that
// names everything missing and the commands that fix it (for the human; the lab never runs them).
// It only reads files and looks up commands. Up calls it and skips the test with its message.
func Preflight() error {
	var missing, fixes []string
	add := func(what, fix string) {
		missing = append(missing, what)
		if fix != "" && !slices.Contains(fixes, fix) {
			fixes = append(fixes, fix)
		}
	}
	if os.Geteuid() != 0 {
		add("root", `run as root: sudo -E env "PATH=$PATH" go test -tags lab ./lab/...`)
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		add("/dev/net/tun", "sudo modprobe tun")
	}
	var pkgs []string
	for _, c := range []struct{ bin, pkg string }{{"ip", "iproute2"}, {"nft", "nftables"}, {"curl", "curl"}} {
		if _, err := exec.LookPath(c.bin); err != nil {
			missing = append(missing, "command "+c.bin)
			pkgs = append(pkgs, c.pkg)
		}
	}
	if len(pkgs) > 0 {
		fixes = append(fixes, "sudo apt-get install "+strings.Join(pkgs, " "))
	}
	mods, _ := os.ReadFile("/proc/modules")
	var absent []string
	for _, m := range []string{"nfnetlink_queue", "nft_queue"} {
		listed := slices.ContainsFunc(strings.Split(string(mods), "\n"), func(l string) bool {
			return strings.HasPrefix(l, m+" ")
		})
		if _, err := os.Stat("/sys/module/" + m); !listed && err != nil {
			missing = append(missing, "kernel module "+m)
			absent = append(absent, m)
		}
	}
	if len(absent) > 0 {
		fixes = append(fixes, "sudo modprobe "+strings.Join(absent, " "))
	}
	if _, err := thirdParty("sing-box"); err != nil {
		add("pinned sing-box (third_party/bin/linux-<arch>/sing-box)", "make fetch")
	}
	if len(missing) == 0 {
		return nil
	}
	return errors.New("lab preflight: missing " + strings.Join(missing, ", ") + "; fix: " + strings.Join(fixes, "; "))
}

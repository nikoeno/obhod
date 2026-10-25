package procman

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/proc"
)

const helperEnv = "PROCMAN_TEST_HELPER"

// TestHelperProcess is not a test: the other tests re-execute the test binary
// with helperEnv set and it plays a child process, so nothing has to be built.
//
//	sleeper       sleeps until killed
//	crasher       exits with status 3 at once
//	slow-stopper  ignores SIGTERM and sleeps (a child that does not stop in time)
//	chatty N      prints N numbered lines, then sleeps
//	manager       runs a Manager with a sleeper child, prints "CHILD <pid>", sleeps
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		t.Skip("helper only")
	}
	switch mode {
	case "sleeper":
		time.Sleep(time.Hour)
	case "crasher":
		fmt.Fprintln(os.Stderr, "crashing")
		os.Exit(3)
	case "slow-stopper":
		signal.Ignore(syscall.SIGTERM)
		fmt.Println("HELPER ready") // SIGTERM is handled from here on; tests wait for this line
		time.Sleep(time.Hour)
	case "chatty":
		n, _ := strconv.Atoi(os.Getenv("HELPER_N"))
		for i := 1; i <= n; i++ {
			fmt.Printf("line %d\n", i)
		}
		time.Sleep(time.Hour)
	case "manager":
		m := New(Config{}, clock.Real(), proc.System())
		if err := m.Start(context.Background(), helperSpec("child", "sleeper")); err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		fmt.Println("CHILD", m.Status()[0].PID)
		time.Sleep(time.Hour)
	}
	os.Exit(0)
}

func helperSpec(name, mode string, env ...string) Spec {
	return Spec{
		Name: name,
		Path: os.Args[0],
		Args: []string{"-test.run=^TestHelperProcess$"},
		Env:  append([]string{helperEnv + "=" + mode}, env...),
	}
}

func newManager(t *testing.T, cfg Config, c clock.Clock) *Manager {
	t.Helper()
	m := New(cfg, c, proc.NewFake())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = m.StopAll(ctx)
	})
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func statusOf(m *Manager, name string) ProcStatus {
	for _, s := range m.Status() {
		if s.Name == name {
			return s
		}
	}
	return ProcStatus{}
}

// startHelperSubprocess runs the "manager" helper and returns it with its child's pid.
func startHelperSubprocess(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"=manager")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	var pid int
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if _, err := fmt.Sscanf(sc.Text(), "CHILD %d", &pid); err == nil {
			return cmd, pid
		}
	}
	t.Fatalf("manager helper did not report a child: %v", sc.Err())
	return nil, 0
}

func pidAlive(t *testing.T, pid int) bool {
	t.Helper()
	ps, err := proc.System().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.PID == pid {
			return true
		}
	}
	return false
}

package sysutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubRestartPlatform 替换平台判断与 os.Exit，返回记录退出码的通道。
// 这些测试改动包级状态，不能并行。
func stubRestartPlatform(t *testing.T, exitRestarts bool) <-chan int {
	t.Helper()
	exits := make(chan int, 1)
	previousSupported, previousExit := restartViaExitSupported, exitProcess
	restartViaExitSupported = func() bool { return exitRestarts }
	exitProcess = func(code int) { exits <- code }
	t.Cleanup(func() {
		restartViaExitSupported, exitProcess = previousSupported, previousExit
	})
	return exits
}

func TestRestartServiceUsesRegisteredGracefulRestartInsteadOfExiting(t *testing.T) {
	exits := stubRestartPlatform(t, true)
	requested := make(chan struct{}, 1)
	unregister := RegisterGracefulRestart(func() { requested <- struct{}{} })
	t.Cleanup(unregister)

	require.NoError(t, RestartService())
	select {
	case <-requested:
	case <-time.After(2 * time.Second):
		t.Fatal("restart was not routed to the registered graceful path")
	}
	select {
	case code := <-exits:
		t.Fatalf("process exited directly with %d despite a registered graceful path", code)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRestartServiceExitsWithZeroWhenNothingIsRegistered(t *testing.T) {
	exits := stubRestartPlatform(t, true)

	require.NoError(t, RestartService())
	select {
	case code := <-exits:
		require.Zero(t, code, "exit code 0 lets systemd Restart=always / Docker unless-stopped restart the process")
	case <-time.After(2 * time.Second):
		t.Fatal("setup-wizard style restart without a registered path must still exit")
	}
}

func TestRestartServiceDoesNothingWhereExitWouldNotRestart(t *testing.T) {
	exits := stubRestartPlatform(t, false)
	requested := make(chan struct{}, 1)
	unregister := RegisterGracefulRestart(func() { requested <- struct{}{} })
	t.Cleanup(unregister)

	require.NoError(t, RestartService())
	select {
	case <-requested:
		t.Fatal("restart requested on a platform without an exit-based supervisor")
	case code := <-exits:
		t.Fatalf("process exited with %d on a platform without an exit-based supervisor", code)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestStaleUnregisterKeepsNewerGracefulRestart(t *testing.T) {
	exits := stubRestartPlatform(t, true)
	first, second := make(chan struct{}, 1), make(chan struct{}, 1)
	unregisterFirst := RegisterGracefulRestart(func() { first <- struct{}{} })
	unregisterSecond := RegisterGracefulRestart(func() { second <- struct{}{} })

	unregisterFirst()
	RequestRestart()
	require.Len(t, second, 1, "an old unregister must not remove the newer registration")
	require.Empty(t, first)

	unregisterSecond()
	RequestRestart()
	require.Equal(t, 0, <-exits, "without a registration the restart falls back to exit(0)")
}

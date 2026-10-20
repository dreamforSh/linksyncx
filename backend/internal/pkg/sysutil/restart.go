// Package sysutil provides system-level utilities for process management.
package sysutil

import (
	"log"
	"os"
	"runtime"
	"sync"
	"time"
)

// restartRegistration 用指针身份区分注册，避免旧的注销函数误删后来的注册。
type restartRegistration struct {
	request func()
}

var (
	restartMu     sync.Mutex
	restartTarget *restartRegistration

	// 以下为测试替身：真实进程中分别是“仅 Linux 支持靠退出重启”和 os.Exit。
	restartViaExitSupported = func() bool { return runtime.GOOS == "linux" }
	exitProcess             = os.Exit
)

// RegisterGracefulRestart lets the main server receive in-app restart requests
// on its graceful shutdown path. It returns a function that removes the
// registration.
//
// 主服务必须注册：直接 os.Exit 会跳过 HTTP 排空和 app.Cleanup，丢掉 usage
// 记录池、计费缓存写队列和配额 flusher 里尚未落盘的计费。request 只负责
// 发出请求（不能阻塞），真正的关停与以退出码 0 退出由主服务完成。
func RegisterGracefulRestart(request func()) (unregister func()) {
	registration := &restartRegistration{request: request}
	restartMu.Lock()
	restartTarget = registration
	restartMu.Unlock()
	return func() {
		restartMu.Lock()
		defer restartMu.Unlock()
		if restartTarget == registration {
			restartTarget = nil
		}
	}
}

func registeredGracefulRestart() func() {
	restartMu.Lock()
	defer restartMu.Unlock()
	if restartTarget == nil {
		return nil
	}
	return restartTarget.request
}

// RestartService triggers a service restart by exiting with status 0.
//
// This relies on the supervisor restarting the process after it exits:
// systemd's Restart=always, Docker's restart: unless-stopped, or the Apple
// container supervisor loop. No sudo permissions or process management are
// needed.
//
// When the main server has registered with RegisterGracefulRestart, the exit
// goes through its graceful shutdown (HTTP drain, then dependency cleanup).
// Otherwise (for example the setup wizard server) the process exits directly.
//
// Prerequisites:
//   - Linux (systemd or a container runtime)
//   - The supervisor restarts the process on exit (e.g. Restart=always)
func RestartService() error {
	if !restartViaExitSupported() {
		log.Println("Service restart via exit only works on Linux with systemd")
		return nil
	}

	log.Println("Initiating service restart by graceful exit...")
	log.Println("systemd will automatically restart the service (Restart=always)")

	// Give a moment for logs to flush and response to be sent
	go func() {
		time.Sleep(100 * time.Millisecond)
		RequestRestart()
	}()

	return nil
}

// RequestRestart immediately asks for a restart without the platform check of
// RestartService: the registered graceful shutdown path when there is one,
// otherwise a direct exit with status 0.
func RequestRestart() {
	if request := registeredGracefulRestart(); request != nil {
		request()
		return
	}
	// 没有主服务注册（安装向导服务没有需要排空的计费状态）：保持原来的直接退出，
	// 退出码 0 仍会被 systemd Restart=always / Docker unless-stopped 拉起。
	exitProcess(0)
}

// RestartServiceAsync is a fire-and-forget version of RestartService.
// It logs errors instead of returning them, suitable for goroutine usage.
func RestartServiceAsync() {
	if err := RestartService(); err != nil {
		log.Printf("Service restart failed: %v", err)
		log.Println("Please restart the service manually: sudo systemctl restart sub2api")
	}
}

package logger

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_DualOutput(t *testing.T) {
	// Use os.MkdirTemp instead of t.TempDir to avoid cleanup failures
	// when lumberjack holds file handles on Windows.
	tmpDir, err := os.MkdirTemp("", "logger-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	logPath := filepath.Join(tmpDir, "logs", "sub2api.log")

	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stderrR.Close()
		_ = stdoutW.Close()
		_ = stderrW.Close()
	})

	err = Init(InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   true,
			FilePath: logPath,
		},
		Rotation: RotationOptions{
			MaxSizeMB:  10,
			MaxBackups: 2,
			MaxAgeDays: 1,
		},
		Sampling: SamplingOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	L().Info("dual-output-info")
	L().Warn("dual-output-warn")

	// Skip Sync() — on Windows, fsync on pipes deadlocks (FlushFileBuffers).
	// The log data is already in the pipe buffer; closing writers is sufficient.
	// The file output is buffered, so flush only the file outputs.
	flushFileOutputs()

	_ = stdoutW.Close()
	_ = stderrW.Close()
	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)
	stdoutText := string(stdoutBytes)
	stderrText := string(stderrBytes)

	if !strings.Contains(stdoutText, "dual-output-info") {
		t.Fatalf("stdout missing info log: %s", stdoutText)
	}
	if !strings.Contains(stderrText, "dual-output-warn") {
		t.Fatalf("stderr missing warn log: %s", stderrText)
	}

	fileBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	fileText := string(fileBytes)
	if !strings.Contains(fileText, "dual-output-info") || !strings.Contains(fileText, "dual-output-warn") {
		t.Fatalf("file missing logs: %s", fileText)
	}
}

func initFileOnlyLoggerForTest(t *testing.T) string {
	t.Helper()
	// Use os.MkdirTemp instead of t.TempDir to avoid cleanup failures
	// when lumberjack holds file handles on Windows.
	tmpDir, err := os.MkdirTemp("", "logger-buffer-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	logPath := filepath.Join(tmpDir, "logs", "sub2api.log")
	err = Init(InitOptions{
		Level:  "info",
		Format: "json",
		Output: OutputOptions{
			ToFile:   true,
			FilePath: logPath,
		},
		Rotation: RotationOptions{
			MaxSizeMB:  10,
			MaxBackups: 1,
			MaxAgeDays: 1,
		},
	})
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	return logPath
}

func readLogFileForTest(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read log file: %v", err)
	}
	return string(data)
}

func TestFileOutput_ErrorFlushesImmediatelyAndInfoAfterFlush(t *testing.T) {
	logPath := initFileOnlyLoggerForTest(t)

	L().Error("file-buffer-error")
	if text := readLogFileForTest(t, logPath); !strings.Contains(text, "file-buffer-error") {
		t.Fatalf("error log should be flushed immediately, got: %s", text)
	}

	L().Info("file-buffer-info")
	flushFileOutputs()
	if text := readLogFileForTest(t, logPath); !strings.Contains(text, "file-buffer-info") {
		t.Fatalf("info log should be written after flush, got: %s", text)
	}
}

func TestReconfigure_ReusesBufferedFileOutput(t *testing.T) {
	logPath := initFileOnlyLoggerForTest(t)
	countOutputs := func() int {
		fileOutputsMu.Lock()
		defer fileOutputsMu.Unlock()
		n := 0
		for key := range fileOutputs {
			if strings.HasPrefix(key, logPath+"|") {
				n++
			}
		}
		return n
	}

	before := L()
	if err := Reconfigure(func(opts *InitOptions) error {
		opts.Level = "debug"
		return nil
	}); err != nil {
		t.Fatalf("Reconfigure() error: %v", err)
	}
	if n := countOutputs(); n != 1 {
		t.Fatalf("reconfigure should reuse the file output, got %d outputs", n)
	}

	before.Info("from-previous-logger")
	L().Debug("from-current-logger")
	flushFileOutputs()
	text := readLogFileForTest(t, logPath)
	if !strings.Contains(text, "from-previous-logger") || !strings.Contains(text, "from-current-logger") {
		t.Fatalf("both logger generations should reach the file, got: %s", text)
	}
}

func TestInit_FileOutputFailureDowngrade(t *testing.T) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	_, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	})

	err = Init(InitOptions{
		Level:  "info",
		Format: "json",
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   true,
			FilePath: filepath.Join(os.DevNull, "logs", "sub2api.log"),
		},
		Rotation: RotationOptions{
			MaxSizeMB:  10,
			MaxBackups: 1,
			MaxAgeDays: 1,
		},
	})
	if err != nil {
		t.Fatalf("Init() should downgrade instead of failing, got: %v", err)
	}

	_ = stderrW.Close()
	stderrBytes, _ := io.ReadAll(stderrR)
	if !strings.Contains(string(stderrBytes), "日志文件输出初始化失败") {
		t.Fatalf("stderr should contain fallback warning, got: %s", string(stderrBytes))
	}
}

func TestInit_CallerShouldPointToCallsite(t *testing.T) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	_, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrW.Close()
	})

	if err := Init(InitOptions{
		Level:       "info",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Caller:      true,
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: SamplingOptions{Enabled: false},
	}); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	L().Info("caller-check")
	// Skip Sync() — on Windows, fsync on pipes deadlocks (FlushFileBuffers).
	os.Stdout = origStdout
	os.Stderr = origStderr
	_ = stdoutW.Close()
	logBytes, _ := io.ReadAll(stdoutR)

	var line string
	for _, item := range strings.Split(string(logBytes), "\n") {
		if strings.Contains(item, "caller-check") {
			line = item
			break
		}
	}
	if line == "" {
		t.Fatalf("log output missing caller-check: %s", string(logBytes))
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("parse log json failed: %v, line=%s", err, line)
	}
	caller, _ := payload["caller"].(string)
	if !strings.Contains(caller, "logger_test.go:") {
		t.Fatalf("caller should point to this test file, got: %s", caller)
	}
}

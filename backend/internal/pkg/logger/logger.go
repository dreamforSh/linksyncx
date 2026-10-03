package logger

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Level = zapcore.Level

const (
	LevelDebug = zapcore.DebugLevel
	LevelInfo  = zapcore.InfoLevel
	LevelWarn  = zapcore.WarnLevel
	LevelError = zapcore.ErrorLevel
	LevelFatal = zapcore.FatalLevel

	// OpsSystemLogSkipField keeps an event in the standard logger while
	// preventing the database-backed Ops system-log sink from indexing it.
	OpsSystemLogSkipField = "ops_system_log_skip"

	// 文件日志攒批写入，避免每条日志一次 write 系统调用；最迟 1 秒落盘。
	fileLogBufferSize          = 256 * 1024
	fileLogBufferFlushInterval = time.Second
)

type Sink interface {
	WriteLogEvent(event *LogEvent)
}

type LogEvent struct {
	Time       time.Time
	Level      string
	Component  string
	Message    string
	LoggerName string
	Fields     map[string]any
}

var (
	mu            sync.RWMutex
	global        atomic.Pointer[zap.Logger]
	sugar         atomic.Pointer[zap.SugaredLogger]
	atomicLevel   zap.AtomicLevel
	initOptions   InitOptions
	currentSink   atomic.Value // sinkState
	stdLogUndo    func()
	bootstrapOnce sync.Once

	// fileOutputs 按"路径 + 轮转参数"复用文件输出。运行时 Reconfigure 只调级别、采样等
	// 选项，新旧 logger 共用同一个缓冲写入器：仍被持有的旧 logger 继续写入也会按时落盘。
	fileOutputsMu sync.Mutex
	fileOutputs   = map[string]*zapcore.BufferedWriteSyncer{}
)

type sinkState struct {
	sink Sink
}

func InitBootstrap() {
	bootstrapOnce.Do(func() {
		if err := Init(bootstrapOptions()); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "logger bootstrap init failed: %v\n", err)
		}
	})
}

func Init(options InitOptions) error {
	mu.Lock()
	defer mu.Unlock()
	return initLocked(options)
}

func initLocked(options InitOptions) error {
	normalized := options.normalized()
	zl, al, err := buildLogger(normalized)
	if err != nil {
		return err
	}

	prev := global.Load()
	global.Store(zl)
	sugar.Store(zl.Sugar())
	atomicLevel = al
	initOptions = normalized

	bridgeSlogLocked()
	bridgeStdLogLocked()

	if prev != nil {
		_ = prev.Sync()
	}
	return nil
}

func Reconfigure(mutator func(*InitOptions) error) error {
	mu.Lock()
	defer mu.Unlock()
	next := initOptions
	if mutator != nil {
		if err := mutator(&next); err != nil {
			return err
		}
	}
	return initLocked(next)
}

func SetLevel(level string) error {
	lv, ok := parseLevel(level)
	if !ok {
		return fmt.Errorf("invalid log level: %s", level)
	}

	mu.Lock()
	defer mu.Unlock()
	atomicLevel.SetLevel(lv)
	initOptions.Level = strings.ToLower(strings.TrimSpace(level))
	return nil
}

func CurrentLevel() string {
	mu.RLock()
	defer mu.RUnlock()
	if global.Load() == nil {
		return "info"
	}
	return atomicLevel.Level().String()
}

func SetSink(sink Sink) {
	currentSink.Store(sinkState{sink: sink})
}

func loadSink() Sink {
	v := currentSink.Load()
	if v == nil {
		return nil
	}
	state, ok := v.(sinkState)
	if !ok {
		return nil
	}
	return state.sink
}

// WriteSinkEvent 直接写入日志 sink，不经过全局日志级别门控。
// 用于需要“可观测性入库”与“业务输出级别”解耦的场景（例如 ops 系统日志索引）。
func WriteSinkEvent(level, component, message string, fields map[string]any) {
	sink := loadSink()
	if sink == nil {
		return
	}

	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		level = "info"
	}
	component = strings.TrimSpace(component)
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}

	eventFields := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		eventFields[k] = v
	}
	if component != "" {
		if _, ok := eventFields["component"]; !ok {
			eventFields["component"] = component
		}
	}

	sink.WriteLogEvent(&LogEvent{
		Time:       time.Now(),
		Level:      level,
		Component:  component,
		Message:    message,
		LoggerName: component,
		Fields:     eventFields,
	})
}

func L() *zap.Logger {
	if l := global.Load(); l != nil {
		return l
	}
	return zap.NewNop()
}

func S() *zap.SugaredLogger {
	if s := sugar.Load(); s != nil {
		return s
	}
	return zap.NewNop().Sugar()
}

func With(fields ...zap.Field) *zap.Logger {
	return L().With(fields...)
}

func Sync() {
	flushFileOutputs()
	l := global.Load()
	if l != nil {
		_ = l.Sync()
	}
}

// flushFileOutputs 把所有文件输出的缓冲写盘；不触碰 stdout/stderr。
func flushFileOutputs() {
	fileOutputsMu.Lock()
	outputs := make([]*zapcore.BufferedWriteSyncer, 0, len(fileOutputs))
	for _, ws := range fileOutputs {
		outputs = append(outputs, ws)
	}
	fileOutputsMu.Unlock()
	for _, ws := range outputs {
		_ = ws.Sync()
	}
}

func bridgeStdLogLocked() {
	if stdLogUndo != nil {
		stdLogUndo()
		stdLogUndo = nil
	}

	prevFlags := log.Flags()
	prevPrefix := log.Prefix()
	prevWriter := log.Writer()

	log.SetFlags(0)
	log.SetPrefix("")
	base := global.Load()
	if base == nil {
		base = zap.NewNop()
	}
	log.SetOutput(newStdLogBridge(base.Named("stdlog")))

	stdLogUndo = func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	}
}

func bridgeSlogLocked() {
	base := global.Load()
	if base == nil {
		base = zap.NewNop()
	}
	slog.SetDefault(slog.New(newSlogZapHandler(base.Named("slog"))))
}

func buildLogger(options InitOptions) (*zap.Logger, zap.AtomicLevel, error) {
	level, _ := parseLevel(options.Level)
	atomic := zap.NewAtomicLevelAt(level)

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var enc zapcore.Encoder
	if options.Format == "console" {
		enc = zapcore.NewConsoleEncoder(encoderCfg)
	} else {
		enc = zapcore.NewJSONEncoder(encoderCfg)
	}

	sinkCore := newSinkCore()
	cores := make([]zapcore.Core, 0, 3)

	if options.Output.ToStdout {
		infoPriority := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= atomic.Level() && lvl < zapcore.WarnLevel
		})
		errPriority := zap.LevelEnablerFunc(func(lvl zapcore.Level) bool {
			return lvl >= atomic.Level() && lvl >= zapcore.WarnLevel
		})
		cores = append(cores, zapcore.NewCore(enc, zapcore.Lock(os.Stdout), infoPriority))
		cores = append(cores, zapcore.NewCore(enc, zapcore.Lock(os.Stderr), errPriority))
	}

	if options.Output.ToFile {
		fileCore, filePath, fileErr := buildFileCore(enc, atomic, options)
		if fileErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "time=%s level=WARN msg=\"日志文件输出初始化失败，降级为仅标准输出\" path=%s err=%v\n",
				time.Now().Format(time.RFC3339Nano),
				filePath,
				fileErr,
			)
		} else {
			cores = append(cores, fileCore)
		}
	}

	if len(cores) == 0 {
		cores = append(cores, zapcore.NewCore(enc, zapcore.Lock(os.Stdout), atomic))
	}

	core := zapcore.NewTee(cores...)
	if options.Sampling.Enabled {
		core = zapcore.NewSamplerWithOptions(core, samplingTick(), options.Sampling.Initial, options.Sampling.Thereafter)
	}
	core = sinkCore.Wrap(core)

	stacktraceLevel, _ := parseStacktraceLevel(options.StacktraceLevel)
	zapOpts := make([]zap.Option, 0, 5)
	if options.Caller {
		zapOpts = append(zapOpts, zap.AddCaller())
	}
	if stacktraceLevel <= zapcore.FatalLevel {
		zapOpts = append(zapOpts, zap.AddStacktrace(stacktraceLevel))
	}

	logger := zap.New(core, zapOpts...).With(
		zap.String("service", options.ServiceName),
		zap.String("env", options.Environment),
	)
	return logger, atomic, nil
}

func buildFileCore(enc zapcore.Encoder, atomic zap.AtomicLevel, options InitOptions) (zapcore.Core, string, error) {
	filePath := options.Output.FilePath
	if strings.TrimSpace(filePath) == "" {
		filePath = resolveLogFilePath("")
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, filePath, err
	}
	ws := fileOutputFor(filePath, options.Rotation)
	return flushOnErrorCore{Core: zapcore.NewCore(enc, ws, atomic), ws: ws}, filePath, nil
}

func fileOutputFor(filePath string, rotation RotationOptions) *zapcore.BufferedWriteSyncer {
	key := fmt.Sprintf("%s|%d|%d|%d|%t|%t", filePath, rotation.MaxSizeMB, rotation.MaxBackups, rotation.MaxAgeDays, rotation.Compress, rotation.LocalTime)
	fileOutputsMu.Lock()
	defer fileOutputsMu.Unlock()
	if ws, ok := fileOutputs[key]; ok {
		return ws
	}
	ws := &zapcore.BufferedWriteSyncer{
		WS: zapcore.AddSync(&lumberjack.Logger{
			Filename:   filePath,
			MaxSize:    rotation.MaxSizeMB,
			MaxBackups: rotation.MaxBackups,
			MaxAge:     rotation.MaxAgeDays,
			Compress:   rotation.Compress,
			LocalTime:  rotation.LocalTime,
		}),
		Size:          fileLogBufferSize,
		FlushInterval: fileLogBufferFlushInterval,
	}
	fileOutputs[key] = ws
	return ws
}

// flushOnErrorCore 在写入 Error 及以上级别后立即刷新缓冲：log.Fatalf 等直接
// os.Exit 的路径会跳过 defer 的 Sync，缓冲里的致命错误不能丢。
type flushOnErrorCore struct {
	zapcore.Core
	ws zapcore.WriteSyncer
}

func (c flushOnErrorCore) With(fields []zapcore.Field) zapcore.Core {
	return flushOnErrorCore{Core: c.Core.With(fields), ws: c.ws}
}

func (c flushOnErrorCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}

func (c flushOnErrorCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	err := c.Core.Write(entry, fields)
	if entry.Level >= zapcore.ErrorLevel {
		_ = c.ws.Sync()
	}
	return err
}

type sinkCore struct {
	core   zapcore.Core
	fields []zapcore.Field
}

func newSinkCore() *sinkCore {
	return &sinkCore{}
}

func (s *sinkCore) Wrap(core zapcore.Core) zapcore.Core {
	cp := *s
	cp.core = core
	return &cp
}

func (s *sinkCore) Enabled(level zapcore.Level) bool {
	return s.core.Enabled(level)
}

func (s *sinkCore) With(fields []zapcore.Field) zapcore.Core {
	nextFields := append([]zapcore.Field{}, s.fields...)
	nextFields = append(nextFields, fields...)
	return &sinkCore{
		core:   s.core.With(fields),
		fields: nextFields,
	}
}

func (s *sinkCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	// Delegate to inner core (tee) so each sub-core's level enabler is respected.
	// Then add ourselves for sink forwarding only.
	ce = s.core.Check(entry, ce)
	if ce != nil {
		ce = ce.AddCore(entry, s)
	}
	return ce
}

func (s *sinkCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// Only handle sink forwarding — the inner cores write via their own
	// Write methods (added to CheckedEntry by s.core.Check above).
	sink := loadSink()
	if sink == nil {
		return nil
	}

	enc := zapcore.NewMapObjectEncoder()
	for _, f := range s.fields {
		f.AddTo(enc)
	}
	for _, f := range fields {
		f.AddTo(enc)
	}

	event := &LogEvent{
		Time:       entry.Time,
		Level:      strings.ToLower(entry.Level.String()),
		Component:  entry.LoggerName,
		Message:    entry.Message,
		LoggerName: entry.LoggerName,
		Fields:     enc.Fields,
	}
	sink.WriteLogEvent(event)
	return nil
}

func (s *sinkCore) Sync() error {
	return s.core.Sync()
}

type stdLogBridge struct {
	logger *zap.Logger
}

func newStdLogBridge(l *zap.Logger) io.Writer {
	if l == nil {
		l = zap.NewNop()
	}
	return &stdLogBridge{logger: l}
}

func (b *stdLogBridge) Write(p []byte) (int, error) {
	msg := normalizeStdLogMessage(string(p))
	if msg == "" {
		return len(p), nil
	}

	level := inferStdLogLevel(msg)
	entry := b.logger.WithOptions(zap.AddCallerSkip(4))

	switch level {
	case LevelDebug:
		entry.Debug(msg, zap.Bool("legacy_stdlog", true))
	case LevelWarn:
		entry.Warn(msg, zap.Bool("legacy_stdlog", true))
	case LevelError, LevelFatal:
		entry.Error(msg, zap.Bool("legacy_stdlog", true))
	default:
		entry.Info(msg, zap.Bool("legacy_stdlog", true))
	}
	return len(p), nil
}

func normalizeStdLogMessage(raw string) string {
	msg := strings.TrimSpace(strings.ReplaceAll(raw, "\n", " "))
	if msg == "" {
		return ""
	}
	return strings.Join(strings.Fields(msg), " ")
}

func inferStdLogLevel(msg string) Level {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return LevelInfo
	}

	if strings.HasPrefix(lower, "[debug]") || strings.HasPrefix(lower, "debug:") {
		return LevelDebug
	}
	if strings.HasPrefix(lower, "[warn]") || strings.HasPrefix(lower, "[warning]") || strings.HasPrefix(lower, "warn:") || strings.HasPrefix(lower, "warning:") {
		return LevelWarn
	}
	if strings.HasPrefix(lower, "[error]") || strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "fatal:") || strings.HasPrefix(lower, "panic:") {
		return LevelError
	}

	if strings.Contains(lower, " failed") || strings.Contains(lower, "error") || strings.Contains(lower, "panic") || strings.Contains(lower, "fatal") {
		return LevelError
	}
	if strings.Contains(lower, "warning") || strings.Contains(lower, "warn") || strings.Contains(lower, " queue full") || strings.Contains(lower, "fallback") {
		return LevelWarn
	}
	return LevelInfo
}

// LegacyPrintf 用于平滑迁移历史的 printf 风格日志到结构化 logger。
func LegacyPrintf(component, format string, args ...any) {
	msg := normalizeStdLogMessage(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}

	initialized := global.Load() != nil
	if !initialized {
		// 在日志系统未初始化前，回退到标准库 log，避免测试/工具链丢日志。
		log.Print(msg)
		return
	}

	l := L()
	if component != "" {
		l = l.With(zap.String("component", component))
	}
	l = l.WithOptions(zap.AddCallerSkip(1))

	switch inferStdLogLevel(msg) {
	case LevelDebug:
		l.Debug(msg, zap.Bool("legacy_printf", true))
	case LevelWarn:
		l.Warn(msg, zap.Bool("legacy_printf", true))
	case LevelError, LevelFatal:
		l.Error(msg, zap.Bool("legacy_printf", true))
	default:
		l.Info(msg, zap.Bool("legacy_printf", true))
	}
}

type contextKey string

const loggerContextKey contextKey = "ctx_logger"

func IntoContext(ctx context.Context, l *zap.Logger) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil {
		l = L()
	}
	return context.WithValue(ctx, loggerContextKey, l)
}

func FromContext(ctx context.Context) *zap.Logger {
	if ctx == nil {
		return L()
	}
	if l, ok := ctx.Value(loggerContextKey).(*zap.Logger); ok && l != nil {
		return l
	}
	return L()
}

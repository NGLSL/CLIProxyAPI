package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/NGLSL/CLIProxyAPI/v7/internal/config"
	"github.com/NGLSL/CLIProxyAPI/v7/internal/util"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	setupOnce      sync.Once
	writerMu       sync.Mutex
	logWriter      *lumberjack.Logger
	ginInfoWriter  *io.PipeWriter
	ginErrorWriter *io.PipeWriter
)

// LogFormatter defines a custom log format for logrus.
// This formatter adds timestamp, level, request ID, and source location to each log entry.
// Format: [2025-12-23 20:14:04] [debug] [manager.go:524] | a1b2c3d4 | Use API key sk-9...0RHO for model gpt-5.2
type LogFormatter struct{}

// logFieldOrder defines the display order for common log fields.
var logFieldOrder = []string{
	"provider", "model",
	"plugin_id", "plugin_name", "source_id",
	"version", "active_version", "retired_version", "overwritten",
	"mode", "budget", "level", "original_mode", "original_value", "min", "max", "clamped_to", "error",
}

var pluginPathFieldOrder = []string{"path", "active_path", "retired_path"}

// Format renders a single log entry with custom formatting.
func (m *LogFormatter) Format(entry *log.Entry) ([]byte, error) {
	var buffer *bytes.Buffer
	if entry.Buffer != nil {
		buffer = entry.Buffer
	} else {
		buffer = &bytes.Buffer{}
	}

	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	message := strings.TrimRight(entry.Message, "\r\n")

	reqID := "--------"
	if id, ok := entry.Data["request_id"].(string); ok && id != "" {
		reqID = id
	}

	level := entry.Level.String()
	if level == "warning" {
		level = "warn"
	}
	levelStr := fmt.Sprintf("%-5s", level)

	// Build fields string (only print fields in logFieldOrder)
	var fieldsStr string
	if len(entry.Data) > 0 {
		var fields []string
		for _, k := range logFieldOrder {
			if v, ok := entry.Data[k]; ok {
				fields = append(fields, fmt.Sprintf("%s=%v", k, v))
			}
		}
		if pluginID, ok := entry.Data["plugin_id"]; ok && strings.TrimSpace(fmt.Sprint(pluginID)) != "" {
			for _, k := range pluginPathFieldOrder {
				if v, ok := entry.Data[k]; ok {
					fields = append(fields, fmt.Sprintf("%s=%v", k, v))
				}
			}
		}
		if len(fields) > 0 {
			fieldsStr = " " + strings.Join(fields, " ")
		}
	}

	var formatted string
	if entry.Caller != nil {
		formatted = fmt.Sprintf("[%s] [%s] [%s] [%s:%d] %s%s\n", timestamp, reqID, levelStr, filepath.Base(entry.Caller.File), entry.Caller.Line, message, fieldsStr)
	} else {
		formatted = fmt.Sprintf("[%s] [%s] [%s] %s%s\n", timestamp, reqID, levelStr, message, fieldsStr)
	}
	buffer.WriteString(formatted)

	return buffer.Bytes(), nil
}

// SetupBaseLogger configures the shared logrus instance and Gin writers.
// It is safe to call multiple times; initialization happens only once.
func SetupBaseLogger() {
	setupOnce.Do(func() {
		log.SetOutput(os.Stdout)
		log.SetReportCaller(true)
		log.SetFormatter(&LogFormatter{})

		ginInfoWriter = log.StandardLogger().Writer()
		gin.DefaultWriter = ginInfoWriter
		ginErrorWriter = log.StandardLogger().WriterLevel(log.ErrorLevel)
		gin.DefaultErrorWriter = ginErrorWriter
		gin.DebugPrintFunc = func(format string, values ...interface{}) {
			format = strings.TrimRight(format, "\r\n")
			log.StandardLogger().Infof(format, values...)
		}

		log.RegisterExitHandler(closeLogOutputs)
	})
}

// isDirWritable checks if the specified directory exists and is writable by attempting to create and remove a test file.
func isDirWritable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}

	testFile := filepath.Join(dir, ".perm_test")
	f, err := os.Create(testFile)
	if err != nil {
		return false
	}

	defer func() {
		_ = f.Close()
		_ = os.Remove(testFile)
	}()
	return true
}

// ResolveLogDirectory determines the directory used for application logs.
func ResolveLogDirectory(cfg *config.Config) string {
	logDir := "logs"
	if base := util.WritablePath(); base != "" {
		return filepath.Join(base, "logs")
	}
	if cfg == nil {
		return logDir
	}
	if !isDirWritable(logDir) {
		authDir, err := util.ResolveAuthDir(cfg.AuthDir)
		if err != nil {
			log.Warnf("Failed to resolve auth-dir %q for log directory: %v", cfg.AuthDir, err)
		}
		if authDir != "" {
			logDir = filepath.Join(authDir, "logs")
		}
	}
	return logDir
}

// ConfigureLogOutput switches the global log destination between rotating files and stdout.
//
// The background logs cleaner always runs so orphan request-log temp files/directories
// (request-body-*.tmp, response-body-*.tmp, request-log-parts-*) are reclaimed.
// When logsMaxTotalSizeMB > 0 it also deletes the oldest durable log artifacts until the
// directory is within the configured budget.
func ConfigureLogOutput(cfg *config.Config) error {
	SetupBaseLogger()

	writerMu.Lock()
	defer writerMu.Unlock()

	logDir := ResolveLogDirectory(cfg)

	protectedPath := ""
	if cfg != nil && cfg.LoggingToFile {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("logging: failed to create log directory: %w", err)
		}
		if logWriter != nil {
			_ = logWriter.Close()
		}
		protectedPath = filepath.Join(logDir, "main.log")
		// Cap rotated main.log backups. MaxBackups=0 in lumberjack means "keep all",
		// which can fill the disk when debug is on under high concurrency. The
		// logs-max-total-size-mb cleaner is the second line of defence for the whole
		// logs directory (request logs + app logs + temp artifacts).
		logWriter = &lumberjack.Logger{
			Filename:   protectedPath,
			MaxSize:    10, // MB per file
			MaxBackups: 50, // hard cap on rotated main.log copies
			MaxAge:     7,  // days
			Compress:   false,
		}
		log.SetOutput(logWriter)
	} else {
		if logWriter != nil {
			_ = logWriter.Close()
			logWriter = nil
		}
		log.SetOutput(os.Stdout)
	}

	maxTotalMB := 0
	if cfg != nil {
		maxTotalMB = cfg.LogsMaxTotalSizeMB
	}
	// request-log writes one full body dump per request. Unlimited retention (0) under
	// high concurrency is a known disk-fill path that can make the host unreachable.
	// When request-log is on and the admin has not set a budget, apply a safety default
	// instead of silently growing forever. Explicit positive values are always honored.
	const defaultRequestLogBudgetMB = 1024
	if cfg != nil && cfg.RequestLog && maxTotalMB <= 0 {
		maxTotalMB = defaultRequestLogBudgetMB
		log.Warnf("logging: request-log is enabled while logs-max-total-size-mb is 0; applying safety budget of %d MB. Set logs-max-total-size-mb explicitly (or disable request-log) for production", defaultRequestLogBudgetMB)
	}
	configureLogDirCleanerLocked(logDir, maxTotalMB, protectedPath)
	return nil
}

func closeLogOutputs() {
	writerMu.Lock()
	defer writerMu.Unlock()

	stopLogDirCleanerLocked()

	if logWriter != nil {
		_ = logWriter.Close()
		logWriter = nil
	}
	if ginInfoWriter != nil {
		_ = ginInfoWriter.Close()
		ginInfoWriter = nil
	}
	if ginErrorWriter != nil {
		_ = ginErrorWriter.Close()
		ginErrorWriter = nil
	}
}

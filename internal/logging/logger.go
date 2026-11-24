package logging

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/security"
)

func getCaller() string {
	var caller string
	if _, file, line, ok := runtime.Caller(2); ok {
		// caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
		caller = fmt.Sprintf("%s:%d", file, line)
	} else {
		caller = "unknown"
	}
	return caller
}
func Info(msg string, args ...any) {
	source := getCaller()
	slog.Info(msg, append([]any{"source", source}, args...)...)
}

func Debug(msg string, args ...any) {
	// slog.Debug(msg, args...)
	source := getCaller()
	slog.Debug(msg, append([]any{"source", source}, args...)...)
}

func Warn(msg string, args ...any) {
	slog.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	slog.Error(msg, args...)
}

func InfoPersist(msg string, args ...any) {
	args = append(args, persistKeyArg, true)
	slog.Info(msg, args...)
}

func DebugPersist(msg string, args ...any) {
	args = append(args, persistKeyArg, true)
	slog.Debug(msg, args...)
}

func WarnPersist(msg string, args ...any) {
	args = append(args, persistKeyArg, true)
	slog.Warn(msg, args...)
}

func ErrorPersist(msg string, args ...any) {
	args = append(args, persistKeyArg, true)
	slog.Error(msg, args...)
}

// RecoverPanic is a common function to handle panics gracefully.
// It logs the error, creates a panic log file with stack trace,
// and executes an optional cleanup function before returning.
func RecoverPanic(name string, cleanup func()) {
	if r := recover(); r != nil {
		if cleanup != nil {
			defer cleanup()
		}
		// Log the panic
		details := security.RedactText(fmt.Sprintf("Panic in %s: %v", name, r))
		ErrorPersist(details)

		// Create a fresh private file so pre-existing files and symbolic links
		// cannot redirect or be overwritten by panic recovery.
		timestamp := time.Now().Format("20060102-150405")
		safeName := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
				return r
			}
			return '_'
		}, name)
		file, err := os.CreateTemp(".", fmt.Sprintf("rsch-panic-%s-%s-*.log", safeName, timestamp))
		if err != nil {
			ErrorPersist(fmt.Sprintf("Failed to create panic log: %v", err))
		} else {
			defer file.Close()

			// Write panic information and stack trace
			fmt.Fprintf(file, "%s\n\n", details)
			fmt.Fprintf(file, "Time: %s\n\n", time.Now().Format(time.RFC3339))
			fmt.Fprintf(file, "Stack Trace:\n%s\n", security.RedactText(string(debug.Stack())))

			InfoPersist(fmt.Sprintf("Panic details written to %s", file.Name()))
		}
	}
}

// Message Logging for Debug
var MessageDir string

func GetSessionPrefix(sessionId string) string {
	return sessionId[:min(8, len(sessionId))]
}

var sessionLogMutex sync.Mutex

func AppendToSessionLogFile(sessionId string, filename string, content string) string {
	if MessageDir == "" || sessionId == "" {
		return ""
	}
	if !safeLogComponent(sessionId) || !safeLogComponent(filename) {
		Error("Refusing invalid session log path")
		return ""
	}
	sessionPrefix := GetSessionPrefix(sessionId)

	sessionLogMutex.Lock()
	defer sessionLogMutex.Unlock()

	root, err := os.OpenRoot(MessageDir)
	if err != nil {
		Error("Failed to open session log root", "error", err)
		return ""
	}
	defer root.Close()
	sessionPath := filepath.Join(MessageDir, sessionPrefix)
	if info, err := root.Lstat(sessionPrefix); err == nil && info.Mode()&os.ModeSymlink != 0 {
		Error("Refusing symbolic-link session directory", "dirpath", sessionPath)
		return ""
	} else if err != nil && !os.IsNotExist(err) {
		Error("Failed to inspect session directory", "dirpath", sessionPath, "error", err)
		return ""
	}
	if _, err := root.Stat(sessionPrefix); os.IsNotExist(err) {
		if err := root.Mkdir(sessionPrefix, 0o700); err != nil {
			Error("Failed to create session directory", "dirpath", sessionPath, "error", err)
			return ""
		}
	}
	sessionRoot, err := root.OpenRoot(sessionPrefix)
	if err != nil {
		Error("Failed to open session directory", "error", err)
		return ""
	}
	defer sessionRoot.Close()
	if err := sessionRoot.Chmod(".", 0o700); err != nil {
		Error("Failed to secure session directory", "dirpath", sessionPath, "error", err)
		return ""
	}

	filePath := filepath.Join(sessionPath, filename)
	if info, err := sessionRoot.Lstat(filename); err == nil && info.Mode()&os.ModeSymlink != 0 {
		Error("Refusing symbolic-link session log file", "filepath", filePath)
		return ""
	} else if err != nil && !os.IsNotExist(err) {
		Error("Failed to inspect session log file", "filepath", filePath, "error", err)
		return ""
	}

	f, err := sessionRoot.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		Error("Failed to open session log file", "filepath", filePath, "error", err)
		return ""
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		Error("Failed to secure session log file", "filepath", filePath, "error", err)
		return ""
	}

	// Append chunk to file
	_, err = f.WriteString(security.RedactText(content))
	if err != nil {
		Error("Failed to write chunk to session log file", "filepath", filePath, "error", err)
		return ""
	}
	return filePath
}

func safeLogComponent(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\:\x00")
}

func WriteRequestMessageJson(sessionId string, requestSeqId int, message any) string {
	if MessageDir == "" || sessionId == "" || requestSeqId <= 0 {
		return ""
	}
	msgJson, err := json.Marshal(message)
	if err != nil {
		Error("Failed to marshal message", "session_id", sessionId, "request_seq_id", requestSeqId, "error", err)
		return ""
	}
	return WriteRequestMessage(sessionId, requestSeqId, string(msgJson))
}

func WriteRequestMessage(sessionId string, requestSeqId int, message string) string {
	if MessageDir == "" || sessionId == "" || requestSeqId <= 0 {
		return ""
	}
	filename := fmt.Sprintf("%d_request.json", requestSeqId)

	return AppendToSessionLogFile(sessionId, filename, message)
}

func AppendToStreamSessionLogJson(sessionId string, requestSeqId int, jsonableChunk any) string {
	if MessageDir == "" || sessionId == "" || requestSeqId <= 0 {
		return ""
	}
	chunkJson, err := json.Marshal(jsonableChunk)
	if err != nil {
		Error("Failed to marshal message", "session_id", sessionId, "request_seq_id", requestSeqId, "error", err)
		return ""
	}
	return AppendToStreamSessionLog(sessionId, requestSeqId, string(chunkJson))
}

func AppendToStreamSessionLog(sessionId string, requestSeqId int, chunk string) string {
	if MessageDir == "" || sessionId == "" || requestSeqId <= 0 {
		return ""
	}
	filename := fmt.Sprintf("%d_response_stream.log", requestSeqId)
	return AppendToSessionLogFile(sessionId, filename, chunk)
}

func WriteChatResponseJson(sessionId string, requestSeqId int, response any) string {
	if MessageDir == "" || sessionId == "" || requestSeqId <= 0 {
		return ""
	}
	responseJson, err := json.Marshal(response)
	if err != nil {
		Error("Failed to marshal response", "session_id", sessionId, "request_seq_id", requestSeqId, "error", err)
		return ""
	}
	filename := fmt.Sprintf("%d_response.json", requestSeqId)

	return AppendToSessionLogFile(sessionId, filename, string(responseJson))
}

func WriteToolResultsJson(sessionId string, requestSeqId int, toolResults any) string {
	if MessageDir == "" || sessionId == "" || requestSeqId <= 0 {
		return ""
	}
	toolResultsJson, err := json.Marshal(toolResults)
	if err != nil {
		Error("Failed to marshal tool results", "session_id", sessionId, "request_seq_id", requestSeqId, "error", err)
		return ""
	}
	filename := fmt.Sprintf("%d_tool_results.json", requestSeqId)
	return AppendToSessionLogFile(sessionId, filename, string(toolResultsJson))
}

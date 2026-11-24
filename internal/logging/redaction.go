package logging

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/opencode-ai/opencode/internal/security"
)

// RedactAttr is a slog.HandlerOptions.ReplaceAttr hook. Applying redaction
// before encoding protects every sink, including direct debug log files.
func RedactAttr(_ []string, attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if sensitiveLogKey(attr.Key) {
		attr.Value = slog.StringValue("[REDACTED]")
		return attr
	}
	if attr.Value.Kind() == slog.KindString {
		attr.Value = slog.StringValue(security.RedactText(attr.Value.String()))
	} else if attr.Value.Kind() == slog.KindAny {
		text := fmt.Sprint(attr.Value.Any())
		if redacted := security.RedactText(text); redacted != text {
			attr.Value = slog.StringValue(redacted)
		}
	}
	return attr
}

func sensitiveLogKey(key string) bool {
	// Text handlers flatten slog groups with periods.
	if index := strings.LastIndexByte(key, '.'); index >= 0 {
		key = key[index+1:]
	}
	key = strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	switch key {
	case "apikey", "secret", "token", "password", "authorization", "proxyauthorization", "accesstoken", "refreshtoken", "clientsecret", "privatekey", "secretaccesskey", "awssecretaccesskey", "cookie", "setcookie":
		return true
	}
	return false
}

func redactLogValue(key, value string) string {
	if sensitiveLogKey(key) {
		return "[REDACTED]"
	}
	return security.RedactText(value)
}

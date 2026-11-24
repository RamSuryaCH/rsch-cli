package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/pubsub"
)

func TestRedactAttrProtectsEncodedLogSinks(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			options := &slog.HandlerOptions{ReplaceAttr: RedactAttr}
			var handler slog.Handler = slog.NewTextHandler(&output, options)
			if format == "json" {
				handler = slog.NewJSONHandler(&output, options)
			}
			logger := slog.New(handler).With("api_key", "secret-api-value")
			logger.Info(`request failed password="secret-message-value"`,
				"count", 7,
				"error", errors.New(`token="secret-error-value"`),
				slog.Group("request", "Authorization", "Basic secret-header-value", "status", "retrying"),
			)
			for _, secret := range []string{"secret-api-value", "secret-message-value", "secret-error-value", "secret-header-value"} {
				if strings.Contains(output.String(), secret) {
					t.Errorf("%s sink exposed credential %q", format, secret)
				}
			}
			if !strings.Contains(output.String(), "[REDACTED]") || !strings.Contains(output.String(), "retrying") {
				t.Fatalf("redaction lost useful log fields: %s", output.String())
			}
		})
	}
}

func TestWriterRedactsCredentialAttributesByKey(t *testing.T) {
	previous := defaultLogData
	defaultLogData = &LogData{Broker: pubsub.NewBroker[LogMessage]()}
	t.Cleanup(func() { defaultLogData = previous })
	logger := slog.New(slog.NewTextHandler(NewWriter(), nil))
	logger.Info("request", "password", "secret-password-value", "api_key", "secret-api-value",
		slog.Group("request", "Authorization", "Bearer secret-bearer-value", "status", "retrying"))
	entries := List()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	for _, attr := range entries[0].Attributes {
		if strings.Contains(attr.Value, "secret-") {
			t.Errorf("credential reached log storage in %q", attr.Key)
		}
		if attr.Key == "request.status" && attr.Value != "retrying" {
			t.Errorf("non-sensitive status changed: %q", attr.Value)
		}
	}
}

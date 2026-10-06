package webhooks

import (
	"fmt"
	"strings"

	logger "github.com/sirupsen/logrus"
)

// Everything in a webhook delivery is chosen by whoever sent it, and the colored
// text formatter prints a log message exactly as it is given, so a line break in
// a logged value would start what reads as a log entry of its own. A delivery
// value therefore reaches the log either through `%q`, which escapes line breaks
// itself, or through the helpers below, which escape them once the message is
// formatted.

// logDebugf logs a debug message built from delivery data.
func logDebugf(format string, args ...any) {
	if logger.IsLevelEnabled(logger.DebugLevel) {
		logger.Debug(escapeLineBreaks(fmt.Sprintf(format, args...)))
	}
}

// logInfof logs an info message built from delivery data.
func logInfof(format string, args ...any) {
	if logger.IsLevelEnabled(logger.InfoLevel) {
		logger.Info(escapeLineBreaks(fmt.Sprintf(format, args...)))
	}
}

// logWarnf logs a warning built from delivery data.
func logWarnf(format string, args ...any) {
	if logger.IsLevelEnabled(logger.WarnLevel) {
		logger.Warn(escapeLineBreaks(fmt.Sprintf(format, args...)))
	}
}

// deliveryFields returns fields fit to log when their values come from a
// delivery: each value formatted the way the text formatter formats it, with its
// line breaks escaped.
func deliveryFields(fields logger.Fields) logger.Fields {
	escaped := make(logger.Fields, len(fields))
	for key, value := range fields {
		escaped[key] = escapeLineBreaks(fmt.Sprint(value))
	}
	return escaped
}

// escapeLineBreaks writes the line breaks in message as `\n` and `\r`.
func escapeLineBreaks(message string) string {
	message = strings.ReplaceAll(message, "\n", `\n`)
	return strings.ReplaceAll(message, "\r", `\r`)
}

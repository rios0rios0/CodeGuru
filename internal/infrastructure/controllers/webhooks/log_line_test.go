package webhooks_test

import (
	"testing"

	logger "github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/infrastructure/controllers/webhooks"
)

// formatCounter counts how many times it is formatted.
type formatCounter struct{ calls int }

func (c *formatCounter) String() string {
	c.calls++
	return "formatted"
}

func TestEscapeLineBreaks(t *testing.T) {
	t.Parallel()

	t.Run("should write line breaks as escape sequences", func(t *testing.T) {
		t.Parallel()
		// given
		message := "enqueued PR #1 in org/repo\nlevel=info msg=forged\r\n"

		// when
		escaped := webhooks.EscapeLineBreaks(message)

		// then
		assert.Equal(t, `enqueued PR #1 in org/repo\nlevel=info msg=forged\r\n`, escaped)
	})

	t.Run("should leave a message without line breaks as it is", func(t *testing.T) {
		t.Parallel()
		// given
		message := "enqueued PR #1 in org/repo"

		// when
		escaped := webhooks.EscapeLineBreaks(message)

		// then
		assert.Equal(t, message, escaped)
	})
}

func TestDeliveryFields(t *testing.T) {
	t.Parallel()

	t.Run("should format every value and escape its line breaks", func(t *testing.T) {
		t.Parallel()
		// given
		fields := logger.Fields{"repo": "org/repo\nforged=1", "pull_id": 42}

		// when
		escaped := webhooks.DeliveryFields(fields)

		// then
		assert.Equal(t, logger.Fields{"repo": `org/repo\nforged=1`, "pull_id": "42"}, escaped)
	})
}

// These tests capture the standard logger, so they do not run in parallel.
func TestLogHelpers(t *testing.T) {
	t.Run("should log a message built from delivery data with its line breaks escaped", func(t *testing.T) {
		// given
		hook := logrustest.NewLocal(logger.StandardLogger())
		defer hook.Reset()
		logger.StandardLogger().SetLevel(logger.DebugLevel)

		// when
		webhooks.LogDebugf("duplicate delivery for PR #%d in %s", 7, "org/repo\nforged")
		webhooks.LogInfof("enqueued PR #%d in %s", 7, "org/repo\nforged")
		webhooks.LogWarnf("rejected %s", "10.0.0.1\r\nforged")

		// then
		entries := hook.AllEntries()
		require.Len(t, entries, 3)
		assert.Equal(t, logger.DebugLevel, entries[0].Level)
		assert.Equal(t, `duplicate delivery for PR #7 in org/repo\nforged`, entries[0].Message)
		assert.Equal(t, logger.InfoLevel, entries[1].Level)
		assert.Equal(t, `enqueued PR #7 in org/repo\nforged`, entries[1].Message)
		assert.Equal(t, logger.WarnLevel, entries[2].Level)
		assert.Equal(t, `rejected 10.0.0.1\r\nforged`, entries[2].Message)
	})

	t.Run("should not format a message the logger's level leaves out", func(t *testing.T) {
		// given
		hook := logrustest.NewLocal(logger.StandardLogger())
		defer hook.Reset()
		original := logger.StandardLogger().GetLevel()
		defer logger.StandardLogger().SetLevel(original)
		logger.StandardLogger().SetLevel(logger.InfoLevel)
		counter := &formatCounter{}

		// when
		webhooks.LogDebugf("skipping %s", counter)

		// then
		assert.Empty(t, hook.AllEntries())
		assert.Zero(t, counter.calls)
	})
}

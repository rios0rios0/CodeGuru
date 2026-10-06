package webhooks_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/infrastructure/controllers/webhooks"
)

func newJob(id int) webhooks.Job {
	return webhooks.Job{
		PR: forgeEntities.PullRequestDetail{
			PullRequest: forgeEntities.PullRequest{ID: id},
		},
	}
}

func TestPool(t *testing.T) {
	t.Parallel()

	t.Run("should consume every submitted job", func(t *testing.T) {
		t.Parallel()

		// given
		var processed atomic.Int32
		done := make(chan struct{}, 10)
		pool := webhooks.NewPool(2, 10, func(_ context.Context, _ webhooks.Job) error {
			processed.Add(1)
			done <- struct{}{}
			return nil
		})

		// when
		for i := range 5 {
			require.NoError(t, pool.Submit(newJob(i)))
		}

		for range 5 {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for job to be processed")
			}
		}

		require.NoError(t, pool.Shutdown(context.Background()))

		// then
		assert.Equal(t, int32(5), processed.Load())
	})

	t.Run("should refuse new work after shutdown", func(t *testing.T) {
		t.Parallel()

		// given
		pool := webhooks.NewPool(1, 5, func(_ context.Context, _ webhooks.Job) error {
			return nil
		})
		require.NoError(t, pool.Shutdown(context.Background()))

		// when
		err := pool.Submit(newJob(1))

		// then
		require.Error(t, err)
		assert.ErrorIs(t, err, webhooks.ErrPoolClosed)
	})

	t.Run("should return ErrPoolFull when the queue is saturated", func(t *testing.T) {
		t.Parallel()

		// given
		release := make(chan struct{})
		pool := webhooks.NewPool(1, 1, func(_ context.Context, _ webhooks.Job) error {
			<-release
			return nil
		})
		// fill the in-flight worker and the queue
		require.NoError(t, pool.Submit(newJob(1)))
		require.Eventually(t, func() bool {
			return pool.Submit(newJob(2)) == nil
		}, time.Second, 10*time.Millisecond, "queue should accept the buffered job once the worker has picked up the first")

		// when
		err := pool.Submit(newJob(3))

		// then
		require.Error(t, err)
		require.ErrorIs(t, err, webhooks.ErrPoolFull)

		close(release)
		require.NoError(t, pool.Shutdown(context.Background()))
	})

	t.Run("should keep running after a handler returns an error", func(t *testing.T) {
		t.Parallel()

		// given
		var processed atomic.Int32
		done := make(chan struct{}, 2)
		pool := webhooks.NewPool(1, 2, func(_ context.Context, j webhooks.Job) error {
			processed.Add(1)
			done <- struct{}{}
			if j.PR.ID == 1 {
				return errors.New("boom")
			}
			return nil
		})

		// when
		require.NoError(t, pool.Submit(newJob(1)))
		require.NoError(t, pool.Submit(newJob(2)))

		for range 2 {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for job to be processed")
			}
		}

		require.NoError(t, pool.Shutdown(context.Background()))

		// then
		assert.Equal(t, int32(2), processed.Load())
	})
}

package webhooks

import (
	"context"
	"errors"
	"sync"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	logger "github.com/sirupsen/logrus"
)

// ErrPoolClosed is returned when work is submitted to a pool that has shut down.
var ErrPoolClosed = errors.New("worker pool is closed")

// ErrPoolFull is returned when the pool's queue is at capacity and cannot accept new work.
var ErrPoolFull = errors.New("worker pool queue is full")

// Job represents a single review request enqueued onto the pool.
type Job struct {
	Provider forgeEntities.ReviewProvider
	Repo     forgeEntities.Repository
	PR       forgeEntities.PullRequestDetail
	CIPassed bool
	// DedupKey is the dedup record acquired by the dispatcher before
	// enqueueing this job. The handler closure in `serve_controller.go`
	// reads it to release the record (`Dispatcher.ReleaseDedup`) after
	// the job finishes — without that release the K8s-Lease backend
	// would leak the lease and block legitimate follow-up pushes for
	// the lifetime of the etcd object. Empty when no dedup backend is
	// wired (purely defensive — the production wiring always sets it).
	DedupKey string
	// UserMentioned signals that this job was triggered by a user
	// comment containing `@code-guru` (the comment-event webhook path).
	// When true, the dispatcher tells the review command to bypass the
	// review-once gate so the user-requested re-review actually runs
	// even if the bot already posted a "review complete" annotation.
	// Push-triggered jobs leave this false so the gate applies and
	// the PR is not flooded on every commit.
	UserMentioned bool
}

// JobHandler processes a single Job. Returning an error logs the failure but does not
// stop the worker.
type JobHandler func(ctx context.Context, job Job) error

// Pool is a bounded worker pool that drains review jobs concurrently.
type Pool struct {
	queue   chan Job
	handler JobHandler
	wg      sync.WaitGroup

	baseCtx    context.Context
	cancelBase context.CancelFunc

	mu     sync.Mutex
	closed bool
}

// NewPool starts workerCount workers reading from a queue of size queueSize.
// queueSize <= 0 defaults to 100; workerCount <= 0 defaults to 1.
func NewPool(workerCount, queueSize int, handler JobHandler) *Pool {
	if queueSize <= 0 {
		queueSize = 100
	}
	if workerCount <= 0 {
		workerCount = 1
	}

	// The cancel function is stored on the pool and invoked by Shutdown via
	// defer; the cross-method release is what makes this safe even though
	// the local lifetime ends here.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	p := &Pool{
		queue:      make(chan Job, queueSize),
		handler:    handler,
		baseCtx:    baseCtx,
		cancelBase: cancelBase,
	}

	for i := range workerCount {
		p.wg.Add(1)
		go p.runWorker(i)
	}
	return p
}

// Submit enqueues a job. Returns ErrPoolClosed if the pool has shut down or
// ErrPoolFull if the queue is full. The send is performed under the same mutex
// that guards Shutdown's close(queue) so the two cannot race.
func (p *Pool) Submit(job Job) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return ErrPoolClosed
	}

	select {
	case p.queue <- job:
		return nil
	default:
		return ErrPoolFull
	}
}

// Shutdown stops accepting new work and waits for in-flight jobs to drain. If ctx
// is cancelled before workers exit, Shutdown cancels the per-job base context so
// in-flight handlers can observe the deadline, then returns the ctx error.
// The base context is always cancelled on return, so calling Shutdown is the
// only way to release the resources held by the pool.
func (p *Pool) Shutdown(ctx context.Context) error {
	defer p.cancelBase()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.queue)
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pool) runWorker(id int) {
	defer p.wg.Done()
	for job := range p.queue {
		p.handle(id, job)
	}
}

func (p *Pool) handle(id int, job Job) {
	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("worker %d recovered from panic processing PR #%d: %v", id, job.PR.ID, r)
		}
	}()

	if err := p.handler(p.baseCtx, job); err != nil {
		logger.Errorf("worker %d failed to process PR #%d: %v", id, job.PR.ID, err)
	}
}

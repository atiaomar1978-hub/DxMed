package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"dxmed/internal/postaction"
)

// RunOptions configure the long-running loops of med-engine.
type RunOptions struct {
	Workers      int
	Poll         time.Duration // idle wait when the queue is empty
	PostEvery    time.Duration
	TickEvery    time.Duration
	RecoverEvery time.Duration
	Scheduler    *Scheduler
	Post         *postaction.Runner
}

// Run starts the workers, the post-action runner, recovery and the scheduler; it returns when ctx ends.
func (e *Engine) Run(ctx context.Context, o RunOptions) {
	if o.Workers <= 0 {
		o.Workers = 2
	}
	var wg sync.WaitGroup
	loop := func(name string, every time.Duration, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				if err := fn(); err != nil && ctx.Err() == nil {
					e.log.Error(name+" failed", "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	if _, _, err := e.Recover(ctx, 2*e.cfg.LeaseTTL); err != nil {
		e.log.Error("recovery failed", "err", err)
	}
	for i := 0; i < o.Workers; i++ {
		worker := fmt.Sprintf("w%d", i+1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				found, err := e.ProcessNext(ctx, worker)
				if err != nil && ctx.Err() == nil {
					e.log.Error("worker failed", "worker", worker, "err", err)
				}
				if !found || err != nil {
					select {
					case <-ctx.Done():
					case <-time.After(o.Poll):
					}
				}
			}
		}()
	}
	if o.Post != nil {
		loop("post actions", o.PostEvery, func() error { _, err := o.Post.Drain(ctx); return err })
	}
	if o.Scheduler != nil {
		loop("scheduler", o.TickEvery, func() error { _, err := o.Scheduler.Tick(ctx); return err })
	}
	loop("recovery", o.RecoverEvery, func() error { _, _, err := e.Recover(ctx, 2*e.cfg.LeaseTTL); return err })
	wg.Wait()
}

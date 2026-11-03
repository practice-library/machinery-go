package runtime

// The deadline: caps.timeSeconds, or the caller's timeout (Connect-Timeout-Ms, or the stdio frame's
// timeoutMs) when it is sooner. The plugin's ctx ends then, and the runtime answers deadline_exceeded at
// that moment whether or not the plugin has returned; a plugin that ignores ctx is left to finish alone.

import (
	"context"
	"fmt"
	"time"
)

// noTimeout is a caller that sent no timeout of its own.
const noTimeout time.Duration = -1

func deadline(caps Caps, timeout time.Duration) time.Duration {
	d := time.Duration(caps.TimeSeconds) * time.Second
	if timeout != noTimeout {
		d = min(d, max(timeout, 0))
	}
	return d
}

type panicked struct{ value any }

func (p panicked) Error() string { return fmt.Sprintf("panic: %v", p.value) }

type outcome struct {
	answer Answer
	err    error
}

// within is fn's outcome, or late when d passes first (fn's ctx then ends with DeadlineExceeded).
// A panic in fn is returned as a panicked error.
func within(ctx context.Context, d time.Duration, fn func(context.Context) (Answer, error)) (a Answer, err error, late bool) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	done := make(chan outcome, 1) // buffered: a late fn still has somewhere to send
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- outcome{err: panicked{v}}
			}
		}()
		a, err := fn(ctx)
		done <- outcome{a, err}
	}()
	select {
	case o := <-done:
		if ctx.Err() != nil { // fn returned as its ctx ended: it is late all the same
			return Answer{}, nil, true
		}
		return o.answer, o.err, false
	case <-ctx.Done(): // the deadline, or a caller that went away
		return Answer{}, nil, true
	}
}

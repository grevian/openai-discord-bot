package bot

import (
	"context"
	"errors"
	"time"
)

// defaultMaxConcurrentImages is how many image generate or edit requests run
// at once. The bot runs in a small memory budget (see the Dockerfile): each
// request holds the source image, the decoded result and the Discord upload
// in memory, so a burst of them is what gets the process OOM-killed.
const defaultMaxConcurrentImages = 3

// defaultImageSlotWait is how long a request waits for a free slot before the
// bot tells the user it is busy.
const defaultImageSlotWait = 20 * time.Second

// msgImagesBusy is the reply when every image slot stayed taken for the whole
// wait.
const msgImagesBusy = "I'm already scribbling away at a pile of other pictures, and my hands only go so fast. Ask me again in a minute. Not that it'll go any better."

// imageSlots is a counting semaphore. A nil imageSlots has no limit.
type imageSlots chan struct{}

func newImageSlots(n int) imageSlots {
	if n <= 0 {
		return nil
	}
	return make(imageSlots, n)
}

// acquire takes a slot, waiting up to wait (no limit when wait is not
// positive, just ctx). It returns a release func that is safe to call once.
// When no slot frees in time it returns a user error carrying the busy reply;
// when ctx ends first it returns ctx's error.
func (s imageSlots) acquire(ctx context.Context, wait time.Duration) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	release := func() { <-s }
	select {
	case s <- struct{}{}:
		return release, nil
	default:
	}
	waitCtx, cancel := withTimeout(ctx, wait)
	defer cancel()
	select {
	case s <- struct{}{}:
		return release, nil
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, newUserError(msgImagesBusy, errors.New("image concurrency limit reached"))
	}
}

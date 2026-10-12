//go:build android || mediacodec_typecheck

package mediacodec

import (
	"context"
	"time"

	"github.com/shibukawa/hwmediacodec/internal/mediacodec/sys"
)

const (
	// inputPoll is how long one AMediaCodec_dequeueInputBuffer waits
	// before the output side is drained and the context checked again.
	inputPoll = 10 * time.Millisecond
	// inputTimeout bounds the wait for an input buffer; a codec that holds
	// every buffer for longer is broken.
	inputTimeout = 3 * time.Second
	// eosTimeout bounds the wait for the end-of-stream marker after Flush.
	eosTimeout = 3 * time.Second
	// maxPending bounds the decoded frames and encoded packets held for
	// Receive; Send reports ErrAgain beyond it.
	maxPending = 32
)

// toMicros converts a timestamp in TimeScale units to microseconds.
func toMicros(ts int64, timeScale int32) int64 {
	scale := int64(timeScale)
	return (ts*1_000_000 + scale/2) / scale
}

// fromMicros converts microseconds back to TimeScale units.
func fromMicros(us int64, timeScale int32) int64 {
	return (us*int64(timeScale) + 500_000) / 1_000_000
}

// dequeueInput waits for an input buffer, draining the output side in
// between so that the codec can recycle its buffers.
func dequeueInput(ctx context.Context, c uintptr, drain func() error) (int, error) {
	deadline := time.Now().Add(inputTimeout)
	for {
		idx := sys.AMediaCodec_dequeueInputBuffer(c, int64(inputPoll/time.Microsecond))
		if idx >= 0 {
			return idx, nil
		}
		if idx != sys.InfoTryAgainLater {
			return 0, backendErr("AMediaCodec_dequeueInputBuffer", int32(idx))
		}
		if err := drain(); err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if time.Now().After(deadline) {
			return 0, backendErr("AMediaCodec_dequeueInputBuffer", sys.ErrorWouldBlock)
		}
	}
}

// queueEOS hands the end-of-stream marker to the codec.
func queueEOS(ctx context.Context, c uintptr, drain func() error) error {
	idx, err := dequeueInput(ctx, c, drain)
	if err != nil {
		return err
	}
	if st := sys.AMediaCodec_queueInputBuffer(c, uintptr(idx), 0, 0, 0, sys.BufferFlagEndOfStream); st != sys.OK {
		return backendErr("AMediaCodec_queueInputBuffer(EOS)", st)
	}
	return nil
}

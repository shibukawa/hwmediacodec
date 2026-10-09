package ebitenvideo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
)

var errStopped = errors.New("ebitenvideo: stopped")

// item is one decoded frame with its position in the stream.
type item struct {
	frame   *hwmediacodec.Frame
	index   int // display index within the loop iteration
	loop    int // loop iteration, from 0
	release func()
}

// source decodes an elementary stream on its own goroutine and hands the
// frames over in display order through a bounded channel, so that decoding
// runs ahead of playback by at most the prefetch depth.
type source struct {
	dec   hwmediacodec.Decoder
	rd    io.Reader
	codec hwmediacodec.Codec
	loop  bool

	items chan item
	stop  chan struct{}
	done  chan struct{}

	mu       sync.Mutex
	err      error
	closed   bool
	finished bool // the channel was observed closed by next
}

func newSource(rd io.Reader, c hwmediacodec.Codec, prefetch int, loop, software bool) (*source, error) {
	if loop {
		if _, ok := rd.(io.Seeker); !ok {
			return nil, errors.New("ebitenvideo: looping needs an io.ReadSeeker source")
		}
	}
	opts := []hwmediacodec.DecoderOption{hwmediacodec.WithOutputFormat(hwmediacodec.RGBA)}
	if software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
	}
	dec, err := hwmediacodec.NewDecoder(context.Background(), c, opts...)
	if err != nil {
		return nil, err
	}
	if prefetch < 1 {
		prefetch = 1
	}
	s := &source{
		dec:   dec,
		rd:    rd,
		codec: c,
		loop:  loop,
		items: make(chan item, prefetch),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go s.run()
	return s, nil
}

func (s *source) run() {
	defer close(s.done)
	defer close(s.items)
	ctx := context.Background()
	for loop := 0; ; loop++ {
		if err := s.decodeOnce(ctx, loop); err != nil {
			if err != errStopped {
				s.setErr(err)
			}
			return
		}
		if !s.loop {
			return
		}
		if _, err := s.rd.(io.Seeker).Seek(0, io.SeekStart); err != nil {
			s.setErr(fmt.Errorf("ebitenvideo: rewind: %w", err))
			return
		}
	}
}

// decodeOnce decodes the stream from the current reader position to its
// end and emits every frame.
func (s *source) decodeOnce(ctx context.Context, loop int) error {
	r := annexb.NewReader(s.rd, s.codec)
	index := 0
	emit := func(f *hwmediacodec.Frame) error {
		select {
		case s.items <- item{frame: f, index: index, loop: loop, release: f.Release}:
			index++
			return nil
		case <-s.stop:
			f.Release()
			return errStopped
		}
	}
	drain := func() error {
		for {
			f, err := s.dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := emit(f); err != nil {
				return err
			}
		}
	}
	var pts int64
	for {
		select {
		case <-s.stop:
			return errStopped
		default:
		}
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := s.dec.Send(ctx, hwmediacodec.Packet{Data: au, PTS: pts}); err != nil {
			return err
		}
		pts++
		if err := drain(); err != nil {
			return err
		}
	}
	if err := s.dec.Flush(ctx); err != nil {
		return err
	}
	return drain()
}

// next returns the next decoded frame without blocking. ok is false when no
// frame is ready yet or the stream has finished (see finished).
func (s *source) next() (item, bool) {
	select {
	case it, ok := <-s.items:
		if !ok {
			s.mu.Lock()
			s.finished = true
			s.mu.Unlock()
			return item{}, false
		}
		return it, true
	default:
		return item{}, false
	}
}

// ended reports whether every frame has been handed out.
func (s *source) ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

func (s *source) setErr(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// Err returns the first decoding error, if any.
func (s *source) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// close stops the goroutine, releases undelivered frames and closes the
// decoder.
func (s *source) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	<-s.done
	for it := range s.items {
		it.release()
	}
	return s.dec.Close()
}

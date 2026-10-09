package mp4_test

import (
	"bytes"
	"io"
)

func bytesReader(b []byte) io.ReadSeeker { return bytes.NewReader(b) }

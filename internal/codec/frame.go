package codec

import "fmt"

// Validate checks that a CPU frame contains every visible row of every
// plane. The last row need not include stride padding. Native frames have
// no CPU planes and cannot be validated with this method.
func (f *Frame) Validate() error {
	if f == nil {
		return fmt.Errorf("%w: nil frame", ErrInvalidData)
	}
	n := f.Format.PlaneCount()
	if n == 0 || f.Format.FrameSize(f.Width, f.Height) == 0 {
		return fmt.Errorf("%w: invalid %s picture size %dx%d", ErrInvalidData, f.Format, f.Width, f.Height)
	}
	if len(f.Planes) != n || len(f.Strides) != n {
		return fmt.Errorf("%w: %s frame needs %d planes and strides, got %d and %d", ErrInvalidData, f.Format, n, len(f.Planes), len(f.Strides))
	}
	for i := 0; i < n; i++ {
		rows, rowBytes := f.Format.PlaneLayout(i, f.Width, f.Height)
		stride, size := f.Strides[i], len(f.Planes[i])
		// Divide the available bytes rather than multiplying an untrusted
		// stride by the number of rows, which could overflow.
		if stride < rowBytes || size < rowBytes || rows-1 > (size-rowBytes)/stride {
			return fmt.Errorf("%w: plane %d holds %d bytes with stride %d, too little for %dx%d", ErrInvalidData, i, size, stride, f.Width, f.Height)
		}
	}
	return nil
}

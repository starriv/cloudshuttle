package nfs3

import (
	"encoding/binary"
	"errors"
	"math"
)

// errShort is a reply that ends before what it must hold.
var errShort = errors.New("nfs: reply too short")

// encoder writes XDR (RFC 4506): big-endian 32-bit units, variable data
// padded to a multiple of four bytes.
type encoder struct{ b []byte }

func (e *encoder) u32(v uint32) { e.b = binary.BigEndian.AppendUint32(e.b, v) }
func (e *encoder) u64(v uint64) { e.b = binary.BigEndian.AppendUint64(e.b, v) }

func (e *encoder) bool(v bool) {
	if v {
		e.u32(1)
	} else {
		e.u32(0)
	}
}

// opaque writes variable-length data with its length.
func (e *encoder) opaque(p []byte) {
	e.u32(uint32(len(p)))
	e.fixed(p)
}

// fixed writes fixed-length data, padded.
func (e *encoder) fixed(p []byte) {
	e.b = append(e.b, p...)
	for len(e.b)%4 != 0 {
		e.b = append(e.b, 0)
	}
}

func (e *encoder) string(s string) { e.opaque([]byte(s)) }

// decoder reads XDR. The first error sticks: later reads return zero
// values, and err says what went wrong, so a reply is read straight through
// and checked once.
type decoder struct {
	b   []byte
	err error
}

func (d *decoder) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || n > len(d.b) {
		d.err = errShort
		return nil
	}
	p := d.b[:n]
	d.b = d.b[n:]
	return p
}

func (d *decoder) u32() uint32 {
	p := d.take(4)
	if p == nil {
		return 0
	}
	return binary.BigEndian.Uint32(p)
}

func (d *decoder) u64() uint64 {
	p := d.take(8)
	if p == nil {
		return 0
	}
	return binary.BigEndian.Uint64(p)
}

func (d *decoder) bool() bool { return d.u32() != 0 }

// opaque reads variable-length data, at most max bytes long.
func (d *decoder) opaque(max int) []byte {
	n := d.u32()
	if d.err != nil {
		return nil
	}
	if n > uint32(max) || n > math.MaxInt32 {
		d.err = errors.New("nfs: item in reply too long")
		return nil
	}
	return d.fixed(int(n))
}

// fixed reads n bytes of fixed-length data and its padding.
func (d *decoder) fixed(n int) []byte {
	p := d.take(n)
	if pad := (4 - n%4) % 4; pad > 0 {
		d.take(pad)
	}
	if d.err != nil {
		return nil
	}
	return p
}

func (d *decoder) string(max int) string { return string(d.opaque(max)) }

package schemagen

import "pgregory.net/rapid"

// Source is the entropy stream a generator draws from. Two implementations
// exist: a rapid-backed Source (reproducible, shrinking) for rapid.Check
// targets, and a byte-backed Source (consumes a []byte, deterministic,
// exhaustion-safe) for the coverage-guided testing.F targets. Abstracting the
// draws lets the same generator definitions drive either backend.
//
// label is preserved on every draw so the rapid-backed Source keeps rapid's
// labelled draws (readable shrink output); the byte Source uses it only for
// self-documentation.
type Source interface {
	Bool(label string) bool
	Intn(n int, label string) int          // [0, n); n<=0 returns 0
	IntRange(lo, hi int, label string) int // inclusive
	Float64Range(lo, hi float64, label string) float64
	String(label string) string
}

// draw is the generic SampledFrom replacement (a method on Source cannot carry
// a type parameter). An empty option slice yields the zero value.
func draw[T any](s Source, opts []T, label string) T {
	if len(opts) == 0 {
		var zero T
		return zero
	}
	return opts[s.Intn(len(opts), label)]
}

// rapidSource delegates each draw to the corresponding rapid primitive, keeping
// the existing rapid.Check targets working unchanged after the refactor.
type rapidSource struct{ t *rapid.T }

func (s rapidSource) Bool(label string) bool { return rapid.Bool().Draw(s.t, label) }

func (s rapidSource) Intn(n int, label string) int {
	if n <= 0 {
		return 0
	}
	return rapid.IntRange(0, n-1).Draw(s.t, label)
}

func (s rapidSource) IntRange(lo, hi int, label string) int {
	if lo >= hi {
		return lo
	}
	return rapid.IntRange(lo, hi).Draw(s.t, label)
}

func (s rapidSource) Float64Range(lo, hi float64, label string) float64 {
	if !(lo < hi) {
		return lo
	}
	return rapid.Float64Range(lo, hi).Draw(s.t, label)
}

func (s rapidSource) String(label string) string { return rapid.String().Draw(s.t, label) }

// byteSource draws entropy from a finite byte slice. When the slice is
// exhausted every draw returns a deterministic default (false, 0, the low end
// of a range, index 0, the empty string) so generation always terminates — the
// generator's depth bound caps structure, so a finite byte budget yields a
// finite value.
type byteSource struct {
	data []byte
	pos  int
}

// NewByteSource wraps data as a Source for coverage-guided testing.F targets:
// the same bytes always produce the same value (determinism), and a short or
// empty slice still yields a well-formed value (exhaustion-safe).
func NewByteSource(data []byte) Source { return &byteSource{data: data} }

func (s *byteSource) next() byte {
	if s.pos >= len(s.data) {
		return 0
	}
	b := s.data[s.pos]
	s.pos++
	return b
}

// readUint consumes nbytes big-endian bytes into a uint64 (zero-padded past
// exhaustion).
func (s *byteSource) readUint(nbytes int) uint64 {
	var v uint64
	for i := 0; i < nbytes; i++ {
		v = v<<8 | uint64(s.next())
	}
	return v
}

func (s *byteSource) Bool(label string) bool { return s.next()&1 == 1 }

func (s *byteSource) Intn(n int, label string) int {
	if n <= 0 {
		return 0
	}
	return int(s.readUint(byteWidth(uint64(n))) % uint64(n))
}

func (s *byteSource) IntRange(lo, hi int, label string) int {
	if lo >= hi {
		return lo
	}
	span := uint64(hi-lo) + 1
	return lo + int(s.readUint(byteWidth(span))%span)
}

func (s *byteSource) Float64Range(lo, hi float64, label string) float64 {
	if !(lo < hi) {
		return lo
	}
	// A 53-bit fraction in [0,1). The midpoint form avoids overflowing hi-lo
	// when the range is the unbounded default (±1e308).
	frac := float64(s.readUint(8)>>11) / float64(uint64(1)<<53)
	mid := lo/2 + hi/2
	half := hi/2 - lo/2
	return mid + (2*frac-1)*half
}

func (s *byteSource) String(label string) string {
	n := int(s.next() % 16) // 0..15 bytes
	if n == 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = s.next()
	}
	return string(b)
}

// byteWidth returns the number of bytes needed to represent n (at least 1).
func byteWidth(n uint64) int {
	w := 1
	for n > 0xff {
		n >>= 8
		w++
	}
	return w
}

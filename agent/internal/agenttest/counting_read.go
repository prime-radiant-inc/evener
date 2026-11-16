// Package agenttest provides shared test scaffolding — scripted fake adapters,
// request/response builders, and a fake execution environment — for tests of
// the agent package and its subpackages.
package agenttest

import "io"

// CountingReadSeekCloser wraps a file so a test can prove how much a read
// actually pulled: Bytes counts the bytes handed over and Reads counts the
// Read calls issued. A bounded read is proven by whichever figure names its
// bound — a byte budget by Bytes, a buffer granularity by Reads — without
// timing anything.
//
// The wrapped file's ReadSeekCloser methods pass through, so a caller can swap
// this in wherever it would hand over the file itself.
type CountingReadSeekCloser struct {
	io.ReadSeekCloser
	Bytes int64
	Reads int
}

func (c *CountingReadSeekCloser) Read(p []byte) (int, error) {
	n, err := c.ReadSeekCloser.Read(p)
	c.Bytes += int64(n)
	c.Reads++
	return n, err
}

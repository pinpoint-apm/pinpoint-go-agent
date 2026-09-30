package ppsarama

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The wrapper exposes CommitTxn and AbortTxn whatever the underlying producer
// has; one without transactions gets an error, not a panic on the assertion.
func Test_asyncProducer_TxnWithoutTransactionsReturnsAnError(t *testing.T) {
	p := wrapAsyncProducer(newStubAsyncProducer(), nil, newConfig())
	defer p.AsyncClose()

	assert.NotPanics(t, func() {
		assert.ErrorIs(t, p.CommitTxn(), errNoTransactions)
		assert.ErrorIs(t, p.AbortTxn(), errNoTransactions)
	})
}

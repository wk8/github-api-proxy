package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// allows mocking TimeNowUnix
// returns a function to cleanup
func WithTimeMock(t *testing.T, times ...int64) func() {
	backup := TimeNowUnix

	index := -1
	TimeNowUnix = func() int64 {
		index++
		require.True(t, index < len(times))
		return times[index]
	}

	return func() {
		TimeNowUnix = backup
		require.Equal(t, len(times)-1, index)
	}
}

package internal

import "time"

// easier to mock, for tests
var TimeNowUnix = func() int64 {
	return time.Now().Unix()
}

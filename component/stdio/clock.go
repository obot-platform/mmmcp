package stdio

import (
	"time"
)

type timer interface {
	Stop() bool
}

type clock interface {
	AfterFunc(time.Duration, func()) timer
}

type realClock struct{}

func (realClock) AfterFunc(delay time.Duration, fn func()) timer {
	return time.AfterFunc(delay, fn)
}

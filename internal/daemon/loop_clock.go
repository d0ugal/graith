package daemon

import "time"

// loopTicker is the small portion of time.Ticker used by daemon background
// loops. Keeping the clock boundary here lets tests drive loops synchronously
// instead of sleeping and hoping the scheduler wins a race.
type loopTicker interface {
	C() <-chan time.Time
	Stop()
}

type realLoopTicker struct {
	ticker *time.Ticker
}

func (t realLoopTicker) C() <-chan time.Time { return t.ticker.C }
func (t realLoopTicker) Stop()               { t.ticker.Stop() }

// loopTimer is the resettable equivalent used by loops with a distinct startup
// delay and steady-state interval.
type loopTimer interface {
	C() <-chan time.Time
	Reset(duration time.Duration)
	Stop()
}

type realLoopTimer struct {
	timer *time.Timer
}

func (t realLoopTimer) C() <-chan time.Time        { return t.timer.C }
func (t realLoopTimer) Reset(d time.Duration)      { t.timer.Reset(d) }
func (t realLoopTimer) Stop()                      { t.timer.Stop() }
func newRealLoopTicker(d time.Duration) loopTicker { return realLoopTicker{ticker: time.NewTicker(d)} }
func newRealLoopTimer(d time.Duration) loopTimer   { return realLoopTimer{timer: time.NewTimer(d)} }

func (sm *SessionManager) loopTicker(d time.Duration) loopTicker {
	if sm.newLoopTicker != nil {
		return sm.newLoopTicker(d)
	}

	return newRealLoopTicker(d)
}

func (sm *SessionManager) loopTimer(d time.Duration) loopTimer {
	if sm.newLoopTimer != nil {
		return sm.newLoopTimer(d)
	}

	return newRealLoopTimer(d)
}

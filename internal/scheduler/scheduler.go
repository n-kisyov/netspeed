package scheduler

import (
	"sync"
	"time"
)

// Scheduler manages periodic background speed tests with safe reconfiguration and shutdown.
type Scheduler struct {
	mu        sync.RWMutex
	enabled   bool
	interval  time.Duration
	nextRun   time.Time
	onTrigger func()

	reconfigCh chan struct{}
	stopCh     chan struct{}
	doneCh     chan struct{}
}

// New creates and starts a Scheduler that calls onTrigger whenever an interval elapses.
func New(onTrigger func()) *Scheduler {
	s := &Scheduler{
		interval:   time.Hour,
		onTrigger:  onTrigger,
		reconfigCh: make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
	go s.loop()
	return s
}

// Configure updates whether scheduled runs are active and the interval between runs.
func (s *Scheduler) Configure(enabled bool, interval time.Duration) {
	if interval < time.Minute {
		interval = time.Minute
	}

	s.mu.Lock()
	s.enabled = enabled
	s.interval = interval
	if enabled {
		s.nextRun = time.Now().Add(interval)
	} else {
		s.nextRun = time.Time{}
	}
	s.mu.Unlock()

	select {
	case s.reconfigCh <- struct{}{}:
	default:
	}
}

// NextRun returns the scheduled timestamp of the next run, or zero if disabled.
func (s *Scheduler) NextRun() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled {
		return time.Time{}
	}
	return s.nextRun
}

// Enabled reports whether scheduled background runs are active.
func (s *Scheduler) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// Interval returns the configured interval.
func (s *Scheduler) Interval() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.interval
}

// Stop terminates the background scheduler goroutine.
func (s *Scheduler) Stop() {
	select {
	case <-s.stopCh:
		return
	default:
		close(s.stopCh)
	}
	<-s.doneCh
}

func (s *Scheduler) loop() {
	defer close(s.doneCh)

	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer = nil
		}
	}
	defer stopTimer()

	for {
		stopTimer()

		s.mu.RLock()
		enabled := s.enabled
		interval := s.interval
		next := s.nextRun
		s.mu.RUnlock()

		var timerCh <-chan time.Time
		if enabled && !next.IsZero() {
			wait := time.Until(next)
			if wait < 0 {
				wait = 0
			}
			timer = time.NewTimer(wait)
			timerCh = timer.C
		}

		select {
		case <-s.stopCh:
			return
		case <-s.reconfigCh:
			continue
		case <-timerCh:
			s.mu.Lock()
			if s.enabled {
				s.nextRun = time.Now().Add(interval)
			}
			cb := s.onTrigger
			s.mu.Unlock()

			if cb != nil {
				go cb()
			}
		}
	}
}

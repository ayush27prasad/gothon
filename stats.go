package gothon

import (
	"sort"
	"time"
)

// Stats is a snapshot of one pool, for health checks and metrics.
type Stats struct {
	Module   string
	Workers  int    // configured worker count
	Idle     int    // ready for a call
	Busy     int    // running a call, including timed-out tasks being drained
	Starting int    // starting up, e.g. replacements
	Waiting  int    // calls waiting for a worker or a task slot
	Restarts uint64 // workers replaced after a crash or a timeout kill
	Recycled uint64 // workers replaced after MaxJobs
	Tasks    map[string]TaskStats
}

// TaskStats counts the calls to one task since the pool started.
type TaskStats struct {
	Calls    uint64        // calls made
	Errors   uint64        // calls that returned an error, of any kind
	Timeouts uint64        // calls whose context ended
	Rejected uint64        // calls refused with ErrBusy
	Running  int           // calls running now
	Waiting  int           // calls waiting now
	AvgTime  time.Duration // moving average of successful run times
}

// Stats returns a snapshot of the pool.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	s := Stats{
		Module:   p.name,
		Workers:  p.workers,
		Idle:     len(p.idle),
		Starting: p.starting,
	}
	s.Busy = len(p.live) - s.Idle - s.Starting
	p.mu.Unlock()
	s.Restarts, s.Recycled = p.restarts.Load(), p.recycled.Load()

	p.tasksMu.Lock()
	defer p.tasksMu.Unlock()
	s.Tasks = make(map[string]TaskStats, len(p.tasks))
	for name, t := range p.tasks {
		ts := TaskStats{
			Calls:    t.calls.Load(),
			Errors:   t.errors.Load(),
			Timeouts: t.timeouts.Load(),
			Rejected: t.rejected.Load(),
			Running:  int(t.running.Load()),
			Waiting:  int(t.waiting.Load()),
			AvgTime:  time.Duration(t.avg.Load()),
		}
		s.Waiting += ts.Waiting
		s.Tasks[name] = ts
	}
	return s
}

// Stats returns a snapshot of every module's pool, ordered by module name.
func (rt *Runtime) Stats() []Stats {
	pools := rt.snapshot()
	out := make([]Stats, len(pools))
	for i, p := range pools {
		out[i] = p.Stats()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

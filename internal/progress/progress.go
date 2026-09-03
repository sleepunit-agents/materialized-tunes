// Package progress is the process-wide list of what is taking time right
// now. Anything that makes the user wait — decoding a catalog, deriving
// classifications, scanning, planning, writing a card, pulling a build —
// starts a Task, moves it as it goes, and ends it. The UI polls Snapshot
// and draws whatever is running: a bar when the total is known, a spinner
// when it isn't, always with a name and a clock.
//
// Why a global and not a parameter: the waits happen deep in packages
// (catalog.read is called from six places) that have no business knowing
// about the UI, and threading a reporter through every signature would
// buy nothing the registry doesn't. Tasks are cheap; Set is a mutex and
// three stores, safe to call per file.
package progress

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Task is one wait in flight, as Snapshot reports it. Total 0 means
// indeterminate. Unit is "bytes", "files", "locations" … — the UI formats
// Done/Total with it. Stage is the phase within the task, free text.
type Task struct {
	ID      int64     `json:"id"`
	Kind    string    `json:"kind"`  // catalog | harvest | reharvest | scan | plan | run | sync | discover | update | library
	Label   string    `json:"label"` // "reading catalog archive"
	Stage   string    `json:"stage,omitempty"`
	Done    int64     `json:"done"`
	Total   int64     `json:"total"`
	Unit    string    `json:"unit,omitempty"`
	Started time.Time `json:"started"`
}

// Running is the handle the worker holds: the task plus its lock.
type Running struct {
	mu sync.Mutex
	t  Task
}

var (
	mu    sync.Mutex
	tasks = map[int64]*Running{}
	next  atomic.Int64
	seq   atomic.Int64 // bumped on every change, so a poller can tell "same" cheaply
)

// Start registers a running task.
func Start(kind, label string) *Running {
	r := &Running{t: Task{ID: next.Add(1), Kind: kind, Label: label, Started: time.Now()}}
	mu.Lock()
	tasks[r.t.ID] = r
	mu.Unlock()
	seq.Add(1)
	return r
}

// Set moves the task: stage (kept when empty), done, total. Nil-safe so
// callers can hold an optional *Running without checking.
func (r *Running) Set(stage string, done, total int64) *Running {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if stage != "" {
		r.t.Stage = stage
	}
	r.t.Done, r.t.Total = done, total
	r.mu.Unlock()
	seq.Add(1)
	return r
}

// Units names what Done/Total count.
func (r *Running) Units(u string) *Running {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.t.Unit = u
	r.mu.Unlock()
	return r
}

// Relabel renames the task mid-flight (a loop over locations names each).
func (r *Running) Relabel(label string) *Running {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.t.Label = label
	r.mu.Unlock()
	seq.Add(1)
	return r
}

// End removes the task. Safe to call twice.
func (r *Running) End() {
	if r == nil {
		return
	}
	mu.Lock()
	delete(tasks, r.t.ID)
	mu.Unlock()
	seq.Add(1)
}

// Snapshot is every running task, oldest first, plus the change sequence.
func Snapshot() (int64, []Task) {
	mu.Lock()
	out := make([]Task, 0, len(tasks))
	for _, r := range tasks {
		r.mu.Lock()
		out = append(out, r.t)
		r.mu.Unlock()
	}
	mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return seq.Load(), out
}

// Seq is the change counter alone.
func Seq() int64 { return seq.Load() }

package server

import "sync"

// frame is one journaled SSE event. Seq is monotonic per session (it survives
// journal resets), so a client's "resume after N" is unambiguous across turns.
type frame struct {
	Seq   uint64
	Event string
	Data  []byte
}

// journal is a bounded ring of frames: one writer (the turn goroutine), many
// readers (attached streams). Older frames are evicted past cap; readers that
// resume from before the window learn via evicted=true to refetch history.
type journal struct {
	mu     sync.Mutex
	frames []frame
	cap    int
	seq    uint64
}

func newJournal(capacity int) *journal { return &journal{cap: capacity} }

func (j *journal) Append(event string, data []byte) frame {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	f := frame{Seq: j.seq, Event: event, Data: data}
	j.frames = append(j.frames, f)
	if len(j.frames) > j.cap {
		j.frames = j.frames[len(j.frames)-j.cap:]
	}
	return f
}

// Since returns retained frames with Seq > after. evicted reports that frames
// between after and the oldest retained one are gone — the caller should
// refetch session history before replaying what remains.
func (j *journal) Since(after uint64) ([]frame, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.frames) > 0 && after < j.frames[0].Seq-1 {
		return append([]frame(nil), j.frames...), true
	}
	var out []frame
	for _, f := range j.frames {
		if f.Seq > after {
			out = append(out, f)
		}
	}
	return out, false
}

// Reset clears retained frames for a new turn; seq keeps rising so resume
// cursors from the previous turn stay meaningful.
func (j *journal) Reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.frames = nil
}

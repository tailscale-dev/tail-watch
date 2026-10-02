package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// memQueue is one sink's bounded FIFO of delivery intents. When it is full the
// oldest event that has not been attempted is dropped to make room.
type memQueue struct {
	limit int
	items []*queued
}

func newMemQueue(limit int) *memQueue {
	return &memQueue{limit: max(limit, 1), items: nil}
}

// Len is the number of queued events, including those in flight.
func (q *memQueue) Len() int { return len(q.items) }

func (q *memQueue) push(it *queued) {
	q.items = append(q.items, it)
}

// Peek returns up to n events from the head without removing them.
func (q *memQueue) Peek(n int) []*queued {
	if n > len(q.items) {
		n = len(q.items)
	}
	return append([]*queued(nil), q.items[:n]...)
}

// Ack removes the events with the given sequence numbers.
func (q *memQueue) Ack(seqs ...uint64) {
	for _, s := range seqs {
		q.Remove(s)
	}
}

// Get returns the event with sequence seq, or nil.
func (q *memQueue) Get(seq uint64) *queued {
	for _, it := range q.items {
		if it.Seq == seq {
			return it
		}
	}
	return nil
}

// Remove deletes the event with sequence seq, if present.
func (q *memQueue) Remove(seq uint64) {
	for i, it := range q.items {
		if it.Seq == seq {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return
		}
	}
}

func (q *memQueue) oldestPending() *queued {
	for _, it := range q.items {
		if !it.Attempted {
			return it
		}
	}
	return nil
}

func (q *memQueue) findRecovery(incidentID string) *queued {
	for _, it := range q.items {
		if it.IncidentID == incidentID && it.Event.Type == EventRecovered {
			return it
		}
	}
	return nil
}

const (
	stateFileName = "notification-state.json"
	stateVersion  = 1
	stateDirMode  = 0o700
	stateFileMode = 0o600
)

// notifyState is the versioned on-disk snapshot: incident records, per-sink
// delivery state, sequence allocation and the pending events of disk sinks. It
// never holds URLs, secrets or headers.
type notifyState struct {
	Version   int                  `json:"version"`
	NextSeq   uint64               `json:"next_seq"`
	Incidents []*incident          `json:"incidents"`
	Queues    map[string][]*queued `json:"queues,omitempty"`
	Units     []*unit              `json:"units,omitempty"`
}

// stateStore writes and reads the snapshot under one directory.
type stateStore struct {
	dir      string
	path     string
	injectMu sync.Mutex
	inject   func() error
}

// setInject makes commits call fn first and fail when it errors. Tests use it.
func (s *stateStore) setInject(fn func() error) {
	s.injectMu.Lock()
	s.inject = fn
	s.injectMu.Unlock()
}

func newStateStore(dir string, log *slog.Logger) (*stateStore, error) {
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return nil, fmt.Errorf("state_dir %s: %w", dir, err)
	}
	if fi, err := os.Stat(dir); err == nil && fi.Mode().Perm()&0o077 != 0 {
		log.Warn("state_dir is readable by group or others", "dir", dir, "mode", fi.Mode().Perm().String())
	}
	s := &stateStore{dir: dir, path: filepath.Join(dir, stateFileName), injectMu: sync.Mutex{}, inject: nil}
	stale, _ := filepath.Glob(filepath.Join(dir, stateFileName+".*.tmp"))
	for _, f := range stale {
		_ = os.Remove(f)
	}
	return s, nil
}

// load reads the committed snapshot. A missing file is an empty state; a file
// that cannot be decoded is an error that leaves the file untouched.
func (s *stateStore) load() (*notifyState, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil //nolint:nilnil // No snapshot yet.
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	var st notifyState
	if unmarshalErr := json.Unmarshal(b, &st); unmarshalErr != nil {
		return nil, fmt.Errorf(
			"notification state %s is corrupt (%w); repair or move the file aside and restart, "+
				"but note open incidents are forgotten if it is removed", s.path, unmarshalErr)
	}
	if st.Version != stateVersion {
		return nil, fmt.Errorf("notification state %s has unsupported version %d", s.path, st.Version)
	}
	return &st, nil
}

// commit atomically replaces the snapshot: write a temporary file, fsync it,
// rename it over the snapshot, then fsync the directory.
func (s *stateStore) commit(st *notifyState) error {
	s.injectMu.Lock()
	inject := s.inject
	s.injectMu.Unlock()
	if inject != nil {
		if err := inject(); err != nil {
			return err
		}
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, stateFileName+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if chmodErr := os.Chmod(name, stateFileMode); chmodErr != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return chmodErr
	}
	if _, writeErr := tmp.Write(data); writeErr != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return writeErr
	}
	if syncErr := tmp.Sync(); syncErr != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return syncErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	if renameErr := os.Rename(name, s.path); renameErr != nil {
		_ = os.Remove(name)
		return renameErr
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// snapshot captures the book for persistence. Memory sinks keep no pending
// payloads on disk, only the incident delivery state.
func (b *book) snapshot() *notifyState {
	st := &notifyState{
		Version: stateVersion, NextSeq: b.nextSeq, Incidents: nil,
		Queues: map[string][]*queued{}, Units: nil,
	}
	for _, id := range slices.Sorted(mapsKeys(b.incidents)) {
		st.Incidents = append(st.Incidents, b.incidents[id])
	}
	for name, cfg := range b.sinks {
		if cfg.Queue == queueDisk {
			st.Queues[name] = append([]*queued{}, b.queues[name].items...)
		}
	}
	for _, key := range slices.Sorted(mapsKeys(b.units)) {
		if u := b.units[key]; b.sinks[u.Sink].Queue == queueDisk {
			st.Units = append(st.Units, u)
		}
	}
	return st
}

// restore replays a committed snapshot: incidents first, then the disk queues,
// then the policies that apply at startup. Memory-sink payloads are gone, so
// they are marked dropped with reason restart. Expiry and queue limits update
// delivery state in the same pass. now decides expiry.
func (b *book) restore(st *notifyState, now time.Time) {
	b.nextSeq = max(st.NextSeq, 1)
	b.restoreIncidents(st.Incidents)
	b.restoreQueues(st.Queues)
	for _, u := range st.Units {
		if q := b.queues[u.Sink]; q != nil {
			b.units[u.key()] = u
		}
	}
	b.clearOrphanAttempts()
	b.loseMemoryIntents()
	for _, name := range slices.Sorted(mapsKeys(b.sinks)) {
		if b.sinks[name].Queue != queueDisk {
			continue
		}
		b.trim(name)
		b.expireStale(name, now)
		b.expireUnits(name, now)
	}
	for _, id := range slices.Sorted(mapsKeys(b.incidents)) {
		b.gc(b.incidents[id])
	}
	for name := range b.queues {
		b.refreshQueueLen(name)
	}
}

func (b *book) restoreIncidents(incs []*incident) {
	for _, inc := range incs {
		if inc.Sinks == nil {
			inc.Sinks = map[string]*sinkDelivery{}
		}
		b.incidents[inc.ID] = inc
		if inc.Status == incidentOpen {
			b.open[inc.Target.Name] = inc.ID
		}
	}
}

// restoreQueues reloads disk-sink queues. A queue whose sink is gone, or is no
// longer a disk sink, is lost and its events are dropped with reason restart.
func (b *book) restoreQueues(queues map[string][]*queued) {
	for _, name := range slices.Sorted(mapsKeys(queues)) {
		cfg, ok := b.sinks[name]
		if !ok || cfg.Queue != queueDisk {
			for _, it := range queues[name] {
				b.dropItem(name, it, reasonRestart)
			}
			continue
		}
		for _, it := range queues[name] {
			b.queues[name].push(it)
		}
	}
}

// clearOrphanAttempts forgets an attempted flag whose unit did not survive.
func (b *book) clearOrphanAttempts() {
	for name, q := range b.queues {
		for _, it := range q.items {
			if it.Attempted && b.units[unitKey(name, it.UnitID)] == nil {
				it.Attempted, it.UnitID = false, ""
			}
		}
	}
}

// loseMemoryIntents marks every delivery whose sink keeps no pending payloads
// across restarts as dropped. Recoveries go first so each lost event counts once.
func (b *book) loseMemoryIntents() {
	for _, id := range slices.Sorted(mapsKeys(b.incidents)) {
		inc := b.incidents[id]
		for name, d := range inc.Sinks {
			if cfg, ok := b.sinks[name]; ok && cfg.Queue == queueDisk {
				continue
			}
			if inc.Recovery != nil && (d.Rec == phasePending || d.Rec == phaseAttempted) {
				b.dropItem(
					name,
					&queued{
						Seq:        0,
						IncidentID: inc.ID,
						Event:      *inc.Recovery,
						Attempted:  false,
						UnitID:     "",
						Mention:    "",
					},
					reasonRestart,
				)
			}
			if d.Down == phasePending || d.Down == phaseAttempted {
				b.dropItem(
					name,
					&queued{Seq: 0, IncidentID: inc.ID, Event: inc.Down, Attempted: false, UnitID: "", Mention: ""},
					reasonRestart,
				)
			}
		}
	}
}

func (b *book) trim(sink string) {
	q := b.queues[sink]
	for q.Len() > q.limit {
		victim := q.oldestPending()
		if victim == nil {
			return
		}
		q.Remove(victim.Seq)
		b.dropItem(sink, victim, reasonQueueFull)
	}
}

func (b *book) expireUnits(sink string, now time.Time) {
	for _, u := range b.sinkUnits(sink) {
		if b.unitExpired(u, now) {
			b.dropUnit(u, reasonExpired)
		}
	}
}

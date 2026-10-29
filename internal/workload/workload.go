// Package workload knows which processes belong to scheduled work rather
// than to the user. A process is a workload if it, or any of its ancestors,
// holds a lease (via `used exec` or the API) or matches a static rule.
//
// Ancestry is the one mechanism shared by every OS; Linux additionally
// matches cgroups, which also covers processes that were reparented.
package workload

import (
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Lease marks a process tree as workload until it expires or its root exits.
type Lease struct {
	ID      string            `json:"id"`
	Tag     string            `json:"tag"`
	PID     int               `json:"pid"`
	Since   time.Time         `json:"since"`
	Expires time.Time         `json:"expires"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// Registry holds leases and static rules. Safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	leases map[string]Lease
	rules  []rule
	maxTTL time.Duration
}

func NewRegistry(rules []Rule, maxTTL time.Duration) (*Registry, error) {
	r := &Registry{leases: map[string]Lease{}, maxTTL: maxTTL}
	for _, raw := range rules {
		c, err := compile(raw)
		if err != nil {
			return nil, err
		}
		r.rules = append(r.rules, c)
	}
	return r, nil
}

// Add creates a lease. ttl 0 means maxTTL; larger values are clamped.
func (r *Registry) Add(tag string, pid int, ttl time.Duration, meta map[string]string) (Lease, error) {
	if pid <= 1 {
		return Lease{}, errors.New("lease needs a real pid")
	}
	if tag == "" {
		tag = fmt.Sprintf("pid-%d", pid)
	}
	if ttl <= 0 || ttl > r.maxTTL {
		ttl = r.maxTTL
	}
	now := time.Now()
	l := Lease{ID: "wl_" + strings.ToLower(rand.Text()[:12]), Tag: tag, PID: pid, Since: now, Expires: now.Add(ttl), Meta: meta}
	r.mu.Lock()
	r.leases[l.ID] = l
	r.mu.Unlock()
	return l, nil
}

func (r *Registry) Remove(id string) (Lease, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.leases[id]
	delete(r.leases, id)
	return l, ok
}

// Prune drops expired leases and leases whose root process is gone.
func (r *Registry) Prune(now time.Time, alive func(pid int) bool) (ended []Lease) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, l := range r.leases {
		if now.After(l.Expires) || !alive(l.PID) {
			ended = append(ended, l)
			delete(r.leases, id)
		}
	}
	return ended
}

func (r *Registry) Leases() []Lease {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Lease, 0, len(r.leases))
	for _, l := range r.leases {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b Lease) int { return a.Since.Compare(b.Since) })
	return out
}

// Empty reports whether nothing could ever match (lets callers skip work).
func (r *Registry) Empty() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.leases) == 0 && len(r.rules) == 0
}

// Owner returns the workload tag owning pid, walking up its ancestry.
func (r *Registry) Owner(pid int, procs *Procs) (tag string, ok bool) {
	if pid <= 0 {
		return "", false
	}
	r.mu.RLock()
	byPID := make(map[int]string, len(r.leases))
	for _, l := range r.leases {
		byPID[l.PID] = l.Tag
	}
	r.mu.RUnlock()

	for cur, depth := pid, 0; cur > 1 && depth < 64; depth++ {
		if tag, ok := byPID[cur]; ok {
			return tag, true
		}
		p := procs.Get(cur)
		for _, ru := range r.rules {
			if ru.match(p) {
				return ru.tag, true
			}
		}
		if p.PPID == cur {
			break
		}
		cur = p.PPID
	}
	return "", false
}

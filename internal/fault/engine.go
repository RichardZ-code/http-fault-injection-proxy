// Package fault allocates independent synchronized decisions after admission.
package fault

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/config"
)

var ErrExhausted = errors.New("rule counter exhausted")

// Decision is a request-owned value, independent of mutable rule state.
// Selected status is an action decision, not a terminal outcome or receipt.
type Decision struct {
	RuleID   string
	Sequence uint64
	Delay    time.Duration
	Status   int
}

type ruleState struct {
	rule     config.Rule
	mu       sync.Mutex
	sequence uint64
	rng      *rand.Rand
}

type Engine struct{ rules []*ruleState }

func New(c config.Config) (*Engine, error) {
	if !c.Valid() {
		return nil, errors.New("validated configuration required")
	}
	e := &Engine{}
	for _, r := range c.Rules() {
		s := &ruleState{rule: r}
		if r.HasProbability {
			prefix := []byte("faultproxy-v1\x00")
			prefix = binary.BigEndian.AppendUint64(prefix, c.Seed())
			prefix = append(prefix, 0)
			hash := sha256.Sum256(append(prefix, r.ID...))
			s.rng = rand.New(rand.NewPCG(binary.BigEndian.Uint64(hash[:8]), binary.BigEndian.Uint64(hash[8:16])))
		}
		e.rules = append(e.rules, s)
	}
	return e, nil
}

func (e *Engine) Allocate(ctx context.Context, method, path string) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		return Decision{RuleID: "none"}, nil
	}
	for _, s := range e.rules {
		r := s.rule
		if (r.Method != "" && r.Method != method) || !strings.HasPrefix(path, r.PathPrefix) {
			continue
		}
		s.mu.Lock()
		// Observe cancellation again after waiting for the allocation lock.
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return Decision{}, err
		}
		if s.sequence == ^uint64(0) {
			s.mu.Unlock()
			return Decision{RuleID: r.ID}, ErrExhausted
		}
		s.sequence++
		d := Decision{RuleID: r.ID, Sequence: s.sequence, Delay: r.Delay}
		selected := r.EveryNth != 0 && s.sequence%r.EveryNth == 0
		if r.HasProbability {
			selected = s.rng.Float64() < r.Probability
		}
		if selected {
			d.Status = r.Status
		}
		s.mu.Unlock()
		return d, nil
	}
	return Decision{RuleID: "none"}, nil
}

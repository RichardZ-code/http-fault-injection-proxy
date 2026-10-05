package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/RichardZ-code/http-fault-injection-proxy/internal/fault"
)

// These dispatch results describe observed action boundaries only. P05/P06
// own terminal transfer classification and completion observations.
type dispatchResult uint8

const (
	forwardRequest dispatchResult = iota
	syntheticStatus
	clientCancelled
	downstreamError
)

func dispatchDecision(w *metadataWriter, r *http.Request, d fault.Decision, decisionReady func(fault.Decision), actions ...func(string)) dispatchResult {
	action := func(kind string) {
		if len(actions) > 0 && actions[0] != nil {
			actions[0](kind)
		}
	}
	if r.Context().Err() != nil {
		return clientCancelled
	}
	if d.Delay > 0 {
		timer := time.NewTimer(d.Delay)
		action("delay")
		defer timer.Stop()
		// Package-private synchronization seam; production installs no callback.
		// The timer is started and allocation's lock has already been released.
		if decisionReady != nil {
			decisionReady(d)
		}
		select {
		case <-r.Context().Done():
			return clientCancelled
		case <-timer.C:
		}
	} else if decisionReady != nil {
		decisionReady(d)
	}
	if r.Context().Err() != nil {
		return clientCancelled
	}
	if d.Status == 0 {
		return forwardRequest
	}
	w.synthetic = true
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	action("status")
	w.WriteHeader(d.Status)
	if r.Method != http.MethodHead {
		if _, err := io.WriteString(w, "fault injected\n"); err != nil {
			return downstreamError
		}
	}
	return syntheticStatus
}

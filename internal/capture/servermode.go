package capture

import "github.com/bonez-io/re_gent/internal/store"

// markLooseObject gives a command-edge delivery integration a chance to queue
// an object that no step references (today an archived host transcript).
func (r *Recorder) markLooseObject(h store.Hash) {
	if r.Delivery != nil && h != "" {
		r.Delivery.QueueObject(r, h)
	}
}

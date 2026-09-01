// Package remotetest provides an in-memory reference implementation of the
// re_gent server's object/ref protocol, plus fault injection.
//
// It exists so that server-mode client code can be tested against the real wire
// protocol — including induced network failures — without a running server.
// Its handlers mirror the F2 demo server: same routes, same status codes, same
// compare-and-swap and reachability rules.
//
// It is a testing helper: only *_test.go files import it.
package remotetest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/bonez-io/re_gent/internal/store"
)

// maxObjectSize mirrors the production server's limit.
const maxObjectSize = 50 << 20

// Fault describes an induced failure applied to one request.
type Fault struct {
	// Status, when non-zero, is returned instead of handling the request.
	Status int
	// Hangup closes the connection without a response, simulating a network
	// blip or a server that dies mid-request.
	Hangup bool
	// Truncate returns a body that is deliberately shorter than the object,
	// simulating a proxy or connection that cuts a response short.
	Truncate bool
}

// Server is an in-memory re_gent server with fault injection.
type Server struct {
	http *httptest.Server

	mu      sync.Mutex
	objects map[store.Hash][]byte
	refs    map[string]store.Hash

	// offline makes every request fail at the transport level.
	offline bool
	// faults is a queue of one-shot faults applied to subsequent requests.
	faults []Fault
	// requests counts handled requests, for asserting on retry behaviour.
	requests map[string]int
	// token, when set, is required as a bearer token.
	token string
}

// New starts a reference server. Callers must Close it.
func New() *Server {
	s := &Server{
		objects:  map[store.Hash][]byte{},
		refs:     map[string]store.Hash{},
		requests: map[string]int{},
	}
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL is the base URL to configure a client with.
func (s *Server) URL() string { return s.http.URL }

// Close shuts the server down.
func (s *Server) Close() { s.http.Close() }

// RequireToken makes the server reject requests without this bearer token.
func (s *Server) RequireToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
}

// SetOffline simulates the server being unreachable: connections are accepted
// and then immediately dropped, which surfaces to the client as a transport
// error, exactly like a network blip.
func (s *Server) SetOffline(offline bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offline = offline
}

// InjectFaults queues one-shot faults, applied in order to the next requests.
func (s *Server) InjectFaults(faults ...Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, faults...)
}

// Requests returns the number of handled requests for a method, e.g. "POST".
func (s *Server) Requests(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[method]
}

// Objects returns a copy of the stored objects.
func (s *Server) Objects() map[store.Hash][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[store.Hash][]byte, len(s.objects))
	for h, data := range s.objects {
		out[h] = data
	}
	return out
}

// Ref returns a stored ref, or "" if absent.
func (s *Server) Ref(name string) store.Hash {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refs[name]
}

// SetRef forces a ref value, for constructing divergence scenarios.
func (s *Server) SetRef(name string, h store.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refs[name] = h
}

// DropObject deletes an object, for constructing partial-write scenarios where
// a ref exists but its contents do not.
func (s *Server) DropObject(h store.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, h)
}

// nextFault pops the next queued fault, if any.
func (s *Server) nextFault() (Fault, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.faults) == 0 {
		return Fault{}, false
	}
	f := s.faults[0]
	s.faults = s.faults[1:]
	return f, true
}

func (s *Server) isOffline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offline
}

func (s *Server) authOK(r *http.Request) bool {
	s.mu.Lock()
	want := s.token
	s.mu.Unlock()
	if want == "" {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+want
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests[r.Method]++
	s.mu.Unlock()

	if s.isOffline() {
		hangup(w)
		return
	}
	if fault, ok := s.nextFault(); ok {
		switch {
		case fault.Hangup:
			hangup(w)
			return
		case fault.Status != 0:
			writeError(w, fault.Status, "injected fault")
			return
		case fault.Truncate:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("truncated"))
			return
		}
	}
	if !s.authOK(r) {
		writeError(w, http.StatusUnauthorized, "bad token")
		return
	}

	// Reject traversal before any path parsing, as the production server does.
	for _, seg := range strings.Split(r.URL.Path, "/") {
		if seg == "." || seg == ".." {
			writeError(w, http.StatusBadRequest, "path contains traversal sequences")
			return
		}
	}

	// Route like the production server: the first path segment is the repo id,
	// then the kind (objects|refs), then the remainder — NOT /repos/{repo}/….
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	kind, rest := parts[1], ""
	if len(parts) == 3 {
		rest = parts[2]
	}

	switch {
	case kind == "objects" && r.Method == http.MethodPut:
		s.putObject(w, r, store.Hash(rest))
	case kind == "objects" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.getObject(w, store.Hash(rest))
	case kind == "refs" && rest == "" && r.Method == http.MethodGet:
		s.listRefs(w, r.URL.Query().Get("dir"))
	case kind == "refs" && r.Method == http.MethodGet:
		s.getRef(w, rest)
	case kind == "refs" && r.Method == http.MethodPost:
		s.postRef(w, r, rest)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request, hash store.Hash) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxObjectSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	if len(data) > maxObjectSize {
		writeError(w, http.StatusRequestEntityTooLarge, "object too large")
		return
	}
	// Like the production server: the body must hash to the address in the URL.
	if got := store.HashBytes(data); got != hash {
		writeError(w, http.StatusBadRequest, "content hash mismatch")
		return
	}

	s.mu.Lock()
	_, existed := s.objects[hash]
	s.objects[hash] = data
	s.mu.Unlock()

	if existed {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func (s *Server) getObject(w http.ResponseWriter, h store.Hash) {
	s.mu.Lock()
	data, ok := s.objects[h]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "object not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

// listRefs mirrors the production server's GET /{repo}/refs[?dir=…]: names come
// back relative to dir, the way store.ListRefs reports them.
func (s *Server) listRefs(w http.ResponseWriter, dir string) {
	prefix := ""
	if dir != "" {
		prefix = strings.TrimSuffix(dir, "/") + "/"
	}

	s.mu.Lock()
	out := map[string]string{}
	for name, h := range s.refs {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		out[strings.TrimPrefix(name, prefix)] = string(h)
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"refs": out})
}

func (s *Server) getRef(w http.ResponseWriter, name string) {
	s.mu.Lock()
	h, ok := s.refs[name]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "ref not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"hash": string(h)})
}

func (s *Server) postRef(w http.ResponseWriter, r *http.Request, name string) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Reachability: refuse to advance a ref to a step whose objects are absent.
	// This is what turns a partial upload into a 422 instead of a dangling ref.
	if err := s.verifyReachableLocked(store.Hash(req.New)); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	current := s.refs[name]
	if string(current) != req.Old {
		writeError(w, http.StatusConflict, "ref was modified concurrently")
		return
	}
	s.refs[name] = store.Hash(req.New)
	writeJSON(w, http.StatusOK, map[string]string{"hash": req.New})
}

// verifyReachableLocked mirrors the production server: the object must exist
// and, if it parses as a step, its tree must exist too.
func (s *Server) verifyReachableLocked(h store.Hash) error {
	data, ok := s.objects[h]
	if !ok {
		return fmt.Errorf("object %s not found", h)
	}
	var step store.Step
	if json.Unmarshal(data, &step) != nil {
		return nil
	}
	if step.Tree == "" {
		return nil
	}
	if _, ok := s.objects[step.Tree]; !ok {
		return fmt.Errorf("step %s references tree %s which has not been pushed", h, step.Tree)
	}
	return nil
}

// hangup closes the connection without writing a response, which the client
// sees as a transport error.
func hangup(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "cannot hijack")
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = conn.Close()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

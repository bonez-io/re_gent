// Package remote defines the interface for communicating with a rgt remote server.
package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bonez-io/re_gent/internal/store"
)

// Remote is the interface for a rgt remote (push/pull target).
type Remote interface {
	// HasObject reports whether the remote already holds the object with the given hash.
	HasObject(h store.Hash) (bool, error)
	// SendObject uploads a raw blob to the remote. Idempotent.
	SendObject(h store.Hash, data []byte) error
	// GetRef reads a named ref from the remote. Returns ("", nil) when the ref
	// does not exist.
	GetRef(name string) (store.Hash, error)
	// UpdateRef advances a ref using compare-and-swap. old may be "" when
	// creating a new ref.
	UpdateRef(name string, old, new store.Hash) error
	// ListRefs returns all refs under the given directory prefix (e.g.
	// "sessions").
	ListRefs(dir string) (map[string]store.Hash, error)
}

// CASConflict is returned by UpdateRef when the remote's current value does
// not match the expected old value.
type CASConflict struct {
	Name    string
	Current store.Hash
}

func (e *CASConflict) Error() string {
	return fmt.Sprintf("ref %s was modified concurrently (current: %s)", e.Name, e.Current)
}

// HTTPRemote talks to a rgt server over HTTP.
type HTTPRemote struct {
	BaseURL string
	Client  *http.Client
}

// NewHTTP creates an HTTPRemote. If client is nil, http.DefaultClient is used.
func NewHTTP(baseURL string, client *http.Client) *HTTPRemote {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPRemote{BaseURL: strings.TrimRight(baseURL, "/"), Client: client}
}

func (r *HTTPRemote) HasObject(h store.Hash) (bool, error) {
	resp, err := r.Client.Head(r.BaseURL + "/objects/" + string(h))
	if err != nil {
		return false, fmt.Errorf("HasObject %s: %w", h, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("HasObject %s: unexpected status %d", h, resp.StatusCode)
	}
}

func (r *HTTPRemote) SendObject(h store.Hash, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, r.BaseURL+"/objects/"+string(h), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("SendObject %s: build request: %w", h, err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("SendObject %s: %w", h, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("SendObject %s: status %d: %s", h, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type refResponse struct {
	Hash string `json:"hash"`
}

func (r *HTTPRemote) GetRef(name string) (store.Hash, error) {
	resp, err := r.Client.Get(r.BaseURL + "/refs/" + name)
	if err != nil {
		return "", fmt.Errorf("GetRef %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GetRef %s: status %d", name, resp.StatusCode)
	}
	var rr refResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return "", fmt.Errorf("GetRef %s: decode: %w", name, err)
	}
	return store.Hash(rr.Hash), nil
}

type updateRefRequest struct {
	Old string `json:"old"`
	New string `json:"new"`
}

func (r *HTTPRemote) UpdateRef(name string, old, new store.Hash) error {
	body, _ := json.Marshal(updateRefRequest{Old: string(old), New: string(new)})
	req, err := http.NewRequest(http.MethodPost, r.BaseURL+"/refs/"+name, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("UpdateRef %s: build request: %w", name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("UpdateRef %s: %w", name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusConflict:
		var cur refResponse
		_ = json.NewDecoder(resp.Body).Decode(&cur)
		return &CASConflict{Name: name, Current: store.Hash(cur.Hash)}
	default:
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("UpdateRef %s: status %d: %s", name, resp.StatusCode, strings.TrimSpace(string(b)))
	}
}

type listRefsResponse struct {
	Refs map[string]string `json:"refs"`
}

func (r *HTTPRemote) ListRefs(dir string) (map[string]store.Hash, error) {
	resp, err := r.Client.Get(r.BaseURL + "/refs?dir=" + dir)
	if err != nil {
		return nil, fmt.Errorf("ListRefs %s: %w", dir, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ListRefs %s: status %d", dir, resp.StatusCode)
	}
	var lr listRefsResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, fmt.Errorf("ListRefs %s: decode: %w", dir, err)
	}
	out := make(map[string]store.Hash, len(lr.Refs))
	for k, v := range lr.Refs {
		out[k] = store.Hash(v)
	}
	return out, nil
}

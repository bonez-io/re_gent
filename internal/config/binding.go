package config

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// RemoteBinding is the [remote] table of a project's committed
// .regent/config.toml — the portable binding `rgt connect` writes (RFC 0001,
// RFC 0004).
//
// ProjectID is the server-generated, opaque project identifier introduced by
// RFC 0004's enrollment API. RepoID is the legacy client-derived identifier
// still written by servers that have not adopted the project API. A binding
// written by a project-id-aware connect carries ProjectID and leaves RepoID
// empty; a binding written against a legacy server carries RepoID and leaves
// ProjectID empty. Both are read here so a file that somehow carries both (a
// hand edit, a downgrade) still resolves to one answer.
type RemoteBinding struct {
	URL       string `toml:"url"`
	ProjectID string `toml:"project_id,omitempty"`
	RepoID    string `toml:"repo_id,omitempty"`
}

// Key is the identifier every storage and protocol call keys on: ProjectID
// when the binding has one, else the legacy RepoID. Every caller that used to
// read RepoID directly should call Key instead, so a project-id binding and a
// legacy repo-id binding are interchangeable to the rest of the program.
func (b RemoteBinding) Key() string {
	if b.ProjectID != "" {
		return b.ProjectID
	}
	return b.RepoID
}

// Connected reports whether the binding names both a server and a project.
// Either alone is not a binding.
func (b RemoteBinding) Connected() bool {
	return b.URL != "" && b.Key() != ""
}

// bindingFile is the shape of the [remote] table alone, used to read and
// write just that table without needing to know every other table the file
// might carry (today: [capture], written by `rgt init` and `rgt connect`).
type bindingFile struct {
	Remote RemoteBinding `toml:"remote"`
}

// LoadRemoteBinding reads the [remote] table of the config.toml at path. A
// missing file returns a zero RemoteBinding and no error, matching every
// other reader of this file: "not connected yet" is not a failure.
func LoadRemoteBinding(path string) (RemoteBinding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RemoteBinding{}, nil
		}
		return RemoteBinding{}, err
	}
	var bf bindingFile
	if err := toml.Unmarshal(data, &bf); err != nil {
		return RemoteBinding{}, err
	}
	return bf.Remote, nil
}

// SaveRemoteBinding writes b's [remote] table into the config.toml at path,
// creating the file and its parent directory if needed.
//
// Every other table already in the file — today, [capture] — is preserved
// byte-for-byte in structure by round-tripping through a generic document
// rather than a fixed struct: this package models only [remote], and a
// struct-shaped rewrite would silently drop a table it does not know about
// (which is exactly the failure store.WriteRepoConfig has, because
// store.RemoteConfig has no field for ProjectID). Merging here is what lets
// `rgt connect` write project_id without erasing the capture acknowledgement
// `rgt init` or an earlier connect already recorded.
func SaveRemoteBinding(path string, b RemoteBinding) error {
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if uerr := toml.Unmarshal(data, &doc); uerr != nil {
			return uerr
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	remote := map[string]any{}
	if b.URL != "" {
		remote["url"] = b.URL
	}
	if b.ProjectID != "" {
		remote["project_id"] = b.ProjectID
	}
	if b.RepoID != "" {
		remote["repo_id"] = b.RepoID
	}
	doc["remote"] = remote

	out, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// ClearRemoteBinding removes the [remote] table from the config.toml at
// path, leaving every other table (such as [capture]) intact. It is the
// project-id-aware counterpart of writing a blank RemoteBinding: `rgt
// disconnect` uses it so a project-id binding is actually erased rather than
// merely zeroed inside a struct that never modelled it.
func ClearRemoteBinding(path string) error {
	doc := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if _, ok := doc["remote"]; !ok {
		return nil
	}
	delete(doc, "remote")
	out, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

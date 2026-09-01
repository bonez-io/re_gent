package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/skills"
)

// registryTimeout bounds one registry request. The user is waiting on a command
// they typed, but a hung server must not hold the terminal: the embedded copy
// is a good answer, so failing over quickly is better than waiting.
const registryTimeout = 5 * time.Second

// maxSkillBytes caps a fetched skill. A SKILL.md is a page of prose; anything
// larger is a misconfigured endpoint or a hostile one, and either way it should
// not be written to the user's disk.
const maxSkillBytes = 256 << 10

// skillOrigin says where an installed skill's text came from. It is reported
// per skill rather than assumed, because "this came off a server" is exactly
// the fact a user needs in order to judge a tool grant.
type skillOrigin string

const (
	originRegistry skillOrigin = "registry"
	originBuiltin  skillOrigin = "built in"
)

// registryURL resolves the server this project should ask for skills.
//
// Deliberately reads only ServerURL, not remote.Config.Enabled(): the registry
// is global, so a repo id is irrelevant to it. A project pointed at a server
// but not yet registered can still install that server's skills.
func registryURL(env remote.Env, cwd, override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	cfg, err := remote.LoadConfigForCWD(env, cwd)
	if err != nil {
		return ""
	}
	return strings.TrimRight(cfg.ServerURL, "/")
}

// fetchSkill asks the registry for one skill's SKILL.md.
func fetchSkill(ctx context.Context, base, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/skills/"+name, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: registryTimeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("the server does not have %q", name)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSkillBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxSkillBytes {
		return "", fmt.Errorf("%s is larger than %d bytes; refusing to install it", name, maxSkillBytes)
	}
	content := string(body)
	// A skill without front matter is not a skill. Writing it would install a
	// file the host loads and cannot act on, and a wrong endpoint (an HTML error
	// page, say) lands here rather than on disk.
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return "", fmt.Errorf("%s does not look like a SKILL.md (no front matter)", name)
	}
	return content, nil
}

// resolveSkill returns a skill's text and where it came from.
//
// The registry wins when one answers, because a skill published to the server
// is the team's current answer while the embedded copy is frozen at whatever
// version of rgt this machine happens to have. The embedded set is the
// fallback, so installing still works with no server and no network.
func resolveSkill(ctx context.Context, base, name string) (content string, origin skillOrigin, err error) {
	if base != "" {
		content, fetchErr := fetchSkill(ctx, base, name)
		if fetchErr == nil {
			return content, originRegistry, nil
		}
		// Fall through to the embedded copy, but keep the reason: if neither has
		// it, the user should hear about both attempts rather than just the last.
		skill, localErr := skills.Get(name)
		if localErr != nil {
			return "", "", fmt.Errorf("%v; and it is not built into this rgt", fetchErr)
		}
		return skill.Content, originBuiltin, nil
	}
	skill, localErr := skills.Get(name)
	if localErr != nil {
		return "", "", localErr
	}
	return skill.Content, originBuiltin, nil
}

// registryEntry is one row of the catalog, as `rgt skill list` renders it.
type registryEntry struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	AllowedTools string `json:"allowed_tools"`
	Source       string `json:"source"`
	Withheld     string `json:"withheld"`
}

// fetchCatalog lists what the registry offers. A failure is not an error the
// caller must handle: `rgt skill list` falls back to the embedded set.
func fetchCatalog(ctx context.Context, base string) ([]registryEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/skills", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: registryTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned %s", resp.Status)
	}
	var body struct {
		Skills []registryEntry `json:"skills"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSkillBytes)).Decode(&body); err != nil {
		return nil, err
	}
	return body.Skills, nil
}

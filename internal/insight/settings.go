package insight

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/store"
)

// Provider names accepted in the two config files.
const (
	ProviderAnthropic        = "anthropic"
	ProviderOpenAICompatible = "openai-compatible"
	ProviderCommand          = "command"
)

// Scrub capture modes accepted in [insight.scrub].capture.
const (
	ScrubCaptureOff          = "off"
	ScrubCaptureSecrets      = "secrets"
	ScrubCaptureSecretsPaths = "secrets+paths"
)

// Defaults for settings a person may leave out.
const (
	DefaultWorkItemIdle   = 2 * time.Hour
	DefaultMaxInputTokens = 24000
)

// Settings is the resolved view of the committed [insight] table and the
// per-user [insight] table together.
type Settings struct {
	// Enabled mirrors the repository's switch.
	Enabled bool
	// WorkItemIdle is the resolved idle window.
	WorkItemIdle time.Duration
	// Scrub is the repository's scrub policy with the capture mode defaulted.
	Scrub store.InsightScrubConfig
	// Model is the user's model provider with the repository's provider-name
	// override applied.
	Model config.InsightModelConfig
	// Embedding is the user's embedding provider.
	Embedding config.InsightEmbeddingConfig
}

// Active reports whether the worker has anything to do here: the repository
// enabled insight and this user configured a model provider. This is the
// answer to RFC 0007's first open question: the repository enables, the
// user's provider config is the opt-in, and a contributor with no provider
// runs nothing and sends nothing.
func (s Settings) Active() bool {
	return s.Enabled && s.Model.Provider != ""
}

// HasEmbedding reports whether an embedding provider is configured. Search
// degrades to full-text without one; the worker still reads work items.
func (s Settings) HasEmbedding() bool {
	return s.Embedding.Provider != ""
}

// Load resolves settings for the repository behind s from its committed
// config and the user's ~/.regent/config.toml.
func Load(s *store.Store) (Settings, error) {
	repo, err := s.ReadRepoConfig()
	if err != nil {
		return Settings{}, err
	}
	user, err := config.Load()
	if err != nil {
		return Settings{}, err
	}
	return Resolve(repo.Insight, user.Insight)
}

// Resolve combines the two tables and validates what they say. A validation
// error names the field and the accepted values; it never echoes a key.
func Resolve(repo store.InsightConfig, user config.InsightUserConfig) (Settings, error) {
	out := Settings{
		Enabled:      repo.Enabled,
		WorkItemIdle: DefaultWorkItemIdle,
		Scrub:        repo.Scrub,
		Model:        user.Model,
		Embedding:    user.Embedding,
	}

	if repo.WorkItemIdle != "" {
		d, err := time.ParseDuration(repo.WorkItemIdle)
		if err != nil || d <= 0 {
			return Settings{}, fmt.Errorf("[insight] work_item_idle %q: want a positive duration such as \"2h\"", repo.WorkItemIdle)
		}
		out.WorkItemIdle = d
	}

	if out.Scrub.Capture == "" {
		out.Scrub.Capture = ScrubCaptureOff
	}
	switch out.Scrub.Capture {
	case ScrubCaptureOff, ScrubCaptureSecrets, ScrubCaptureSecretsPaths:
	default:
		return Settings{}, fmt.Errorf("[insight.scrub] capture %q: want %q, %q, or %q",
			out.Scrub.Capture, ScrubCaptureOff, ScrubCaptureSecrets, ScrubCaptureSecretsPaths)
	}

	if repo.Model.Provider != "" {
		out.Model.Provider = repo.Model.Provider
	}
	if out.Model.MaxInputTokens == 0 {
		out.Model.MaxInputTokens = DefaultMaxInputTokens
	}
	if out.Model.MaxInputTokens < 0 {
		return Settings{}, fmt.Errorf("[insight.model] max_input_tokens %d: want a positive number", out.Model.MaxInputTokens)
	}

	if err := validateModel(out.Model); err != nil {
		return Settings{}, err
	}
	if err := validateEmbedding(out.Embedding); err != nil {
		return Settings{}, err
	}
	return out, nil
}

func validateModel(m config.InsightModelConfig) error {
	switch m.Provider {
	case "":
		return nil
	case ProviderAnthropic:
		if m.Model == "" {
			return errors.New("[insight.model] model is required for the anthropic provider")
		}
	case ProviderOpenAICompatible:
		if m.Model == "" {
			return errors.New("[insight.model] model is required for the openai-compatible provider")
		}
		if m.BaseURL == "" {
			return errors.New("[insight.model] base_url is required for the openai-compatible provider")
		}
	case ProviderCommand:
		if len(m.Command) == 0 {
			return errors.New("[insight.model] command is required for the command provider")
		}
	default:
		return fmt.Errorf("[insight.model] provider %q: want %q, %q, or %q",
			m.Provider, ProviderAnthropic, ProviderOpenAICompatible, ProviderCommand)
	}
	return nil
}

func validateEmbedding(e config.InsightEmbeddingConfig) error {
	switch e.Provider {
	case "":
		return nil
	case ProviderOpenAICompatible:
		if e.Model == "" {
			return errors.New("[insight.embedding] model is required for the openai-compatible provider")
		}
		if e.BaseURL == "" {
			return errors.New("[insight.embedding] base_url is required for the openai-compatible provider")
		}
	case ProviderCommand:
		if len(e.Command) == 0 {
			return errors.New("[insight.embedding] command is required for the command provider")
		}
	case ProviderAnthropic:
		return errors.New("[insight.embedding] provider \"anthropic\": Anthropic has no embeddings endpoint; use openai-compatible or command")
	default:
		return fmt.Errorf("[insight.embedding] provider %q: want %q or %q",
			e.Provider, ProviderOpenAICompatible, ProviderCommand)
	}
	if e.Dimensions < 0 {
		return fmt.Errorf("[insight.embedding] dimensions %d: want a positive number", e.Dimensions)
	}
	return nil
}

// KeyState describes whether a provider's key variable is usable, without
// ever reading the value into anything that is printed or stored.
type KeyState struct {
	// Env is the variable's name; empty when the provider needs no key.
	Env string
	// Set is whether the variable is present and non-empty right now.
	Set bool
}

// String renders the state for status output.
func (k KeyState) String() string {
	switch {
	case k.Env == "":
		return "no key needed"
	case k.Set:
		return k.Env + " is set"
	default:
		return k.Env + " is not set"
	}
}

// ModelKey reports the model provider's key variable state.
func (s Settings) ModelKey() KeyState { return keyState(s.Model.APIKeyEnv) }

// EmbeddingKey reports the embedding provider's key variable state.
func (s Settings) EmbeddingKey() KeyState { return keyState(s.Embedding.APIKeyEnv) }

func keyState(env string) KeyState {
	env = strings.TrimSpace(env)
	if env == "" {
		return KeyState{}
	}
	return KeyState{Env: env, Set: os.Getenv(env) != ""}
}

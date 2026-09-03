package insight

import (
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/store"
)

func TestResolve_DefaultsAndActive(t *testing.T) {
	got, err := Resolve(store.InsightConfig{}, config.InsightUserConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Active() || got.Enabled || got.HasEmbedding() {
		t.Fatalf("zero config must be inactive: %#v", got)
	}
	if got.WorkItemIdle != DefaultWorkItemIdle || got.Scrub.Capture != ScrubCaptureOff || got.Model.MaxInputTokens != DefaultMaxInputTokens {
		t.Fatalf("defaults not applied: %#v", got)
	}

	// Repository enabled, user has no provider: enabled but not active.
	got, err = Resolve(store.InsightConfig{Enabled: true}, config.InsightUserConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Active() {
		t.Fatalf("repo-only must be enabled but inactive: %#v", got)
	}

	// Both halves present: active.
	got, err = Resolve(
		store.InsightConfig{Enabled: true, WorkItemIdle: "30m", Scrub: store.InsightScrubConfig{Capture: "secrets", Patterns: []string{"ACME"}}},
		config.InsightUserConfig{
			Model:     config.InsightModelConfig{Provider: ProviderAnthropic, Model: "claude-haiku-4-5-20251001", APIKeyEnv: "ANTHROPIC_API_KEY"},
			Embedding: config.InsightEmbeddingConfig{Provider: ProviderOpenAICompatible, Model: "nomic-embed-text", BaseURL: "http://localhost:11434/v1"},
		})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active() || !got.HasEmbedding() || got.WorkItemIdle != 30*time.Minute || got.Scrub.Patterns[0] != "ACME" {
		t.Fatalf("resolved: %#v", got)
	}
}

func TestResolve_RepoProviderNameOverride(t *testing.T) {
	got, err := Resolve(
		store.InsightConfig{Enabled: true, Model: store.InsightModelOverride{Provider: ProviderCommand}},
		config.InsightUserConfig{Model: config.InsightModelConfig{Provider: ProviderAnthropic, Model: "x", Command: []string{"claude", "-p"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model.Provider != ProviderCommand {
		t.Fatalf("repo override should win on the name: %q", got.Model.Provider)
	}
}

func TestResolve_Validation(t *testing.T) {
	cases := []struct {
		name string
		repo store.InsightConfig
		user config.InsightUserConfig
		want string
	}{
		{"bad idle", store.InsightConfig{WorkItemIdle: "soon"}, config.InsightUserConfig{}, "work_item_idle"},
		{"bad scrub", store.InsightConfig{Scrub: store.InsightScrubConfig{Capture: "all"}}, config.InsightUserConfig{}, "[insight.scrub] capture"},
		{"unknown model provider", store.InsightConfig{}, config.InsightUserConfig{Model: config.InsightModelConfig{Provider: "gemini"}}, "[insight.model] provider"},
		{"anthropic without model", store.InsightConfig{}, config.InsightUserConfig{Model: config.InsightModelConfig{Provider: ProviderAnthropic}}, "model is required"},
		{"openai without url", store.InsightConfig{}, config.InsightUserConfig{Model: config.InsightModelConfig{Provider: ProviderOpenAICompatible, Model: "m"}}, "base_url is required"},
		{"command without argv", store.InsightConfig{}, config.InsightUserConfig{Model: config.InsightModelConfig{Provider: ProviderCommand}}, "command is required"},
		{"anthropic embeddings", store.InsightConfig{}, config.InsightUserConfig{Embedding: config.InsightEmbeddingConfig{Provider: ProviderAnthropic}}, "no embeddings endpoint"},
		{"negative dims", store.InsightConfig{}, config.InsightUserConfig{Embedding: config.InsightEmbeddingConfig{Provider: ProviderCommand, Command: []string{"x"}, Dimensions: -1}}, "dimensions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(tc.repo, tc.user)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestKeyState_NeverPrintsValue(t *testing.T) {
	t.Setenv("RGT_TEST_INSIGHT_KEY", "sk-secret-value")
	s := Settings{Model: config.InsightModelConfig{APIKeyEnv: "RGT_TEST_INSIGHT_KEY"}}
	if got := s.ModelKey().String(); got != "RGT_TEST_INSIGHT_KEY is set" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(s.ModelKey().String(), "secret") {
		t.Fatal("key value leaked into status text")
	}
	s.Model.APIKeyEnv = "RGT_TEST_INSIGHT_KEY_MISSING"
	if got := s.ModelKey().String(); got != "RGT_TEST_INSIGHT_KEY_MISSING is not set" {
		t.Fatalf("got %q", got)
	}
	if got := (Settings{}).EmbeddingKey().String(); got != "no key needed" {
		t.Fatalf("got %q", got)
	}
}

func TestLoad_ReadsBothFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := config.Save(&config.UserConfig{Insight: config.InsightUserConfig{
		Model: config.InsightModelConfig{Provider: ProviderCommand, Command: []string{"claude", "-p"}},
	}}); err != nil {
		t.Fatal(err)
	}

	s, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteRepoConfig(store.RepoConfig{Insight: store.InsightConfig{Enabled: true}}); err != nil {
		t.Fatal(err)
	}

	got, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active() || got.Model.Command[0] != "claude" {
		t.Fatalf("loaded: %#v", got)
	}
}

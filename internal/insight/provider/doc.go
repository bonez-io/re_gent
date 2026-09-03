// Package provider implements the model and embedding providers RFC 0007
// stream S3 defines: anthropic, openai-compatible, and command. It is the
// only place in re_gent that speaks to a model provider's HTTP API or a
// local command, so timeouts, retry, and reply extraction live here once
// instead of being reimplemented per caller.
//
// Constructors take the resolved config.InsightModelConfig or
// config.InsightEmbeddingConfig (see internal/insight/settings.go for
// validation) and never perform network I/O themselves; a key read from the
// environment happens at call time, not at construction, so a provider can
// be built before a secret is exported and so a key is never captured in a
// long-lived struct.
package provider

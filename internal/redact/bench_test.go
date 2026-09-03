package redact

import (
	"strings"
	"testing"
)

// buildBenchContent builds ~5MB of realistic-ish mixed content: prose,
// hex-looking hashes/shas (which must NOT match, and so exercise the full
// regex set without short-circuiting), and a handful of real secrets
// sprinkled in, similar to what a large tool-result blob might contain.
func buildBenchContent(targetSize int) []byte {
	unit := strings.Join([]string{
		"The quick brown fox jumps over the lazy dog while reviewing pull request #482.",
		"commit e3b0c44298fc1c149afbf4c8996fb92427ae41e4 touched internal/store/blob.go",
		"object 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef stored",
		"See https://example.com/docs/getting-started?ref=readme&utm_source=github for details.",
		"",
	}, "\n")

	secretUnit := "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\npassword = \"Tr0ub4dor&3xyz\"\n"

	var b strings.Builder
	b.Grow(targetSize + len(unit) + len(secretUnit))
	i := 0
	for b.Len() < targetSize {
		b.WriteString(unit)
		// Sprinkle in a handful of real secrets, not one every unit — a
		// blob that is nothing but back-to-back credentials isn't a
		// realistic tool-result/file snapshot, and its cost is dominated
		// by output size, not detector overhead.
		if i%50 == 0 {
			b.WriteString(secretUnit)
		}
		i++
	}
	return []byte(b.String())
}

func BenchmarkDetect_5MB(b *testing.B) {
	content := buildBenchContent(5 * 1024 * 1024)
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Detect(content)
	}
}

func BenchmarkRedact_5MB(b *testing.B) {
	content := buildBenchContent(5 * 1024 * 1024)
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Redact(content, Options{})
	}
}

func BenchmarkHomePaths_5MB(b *testing.B) {
	content := buildBenchContent(5 * 1024 * 1024)
	homes := []string{"/Users/shay"}
	users := []string{"shay"}
	b.SetBytes(int64(len(content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = HomePaths(content, homes, users)
	}
}

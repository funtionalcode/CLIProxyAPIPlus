package translator

import (
	"context"
	"errors"
	"testing"
)

func TestPipelineReturnsRequestTranslationFailure(t *testing.T) {
	r := NewRegistry()
	want := errors.New("unsupported attachment")
	r.Register(FormatOpenAI, FormatClaude, func(string, []byte, bool) ([]byte, error) {
		return nil, want
	}, ResponseTransform{})
	_, err := NewPipeline(r).TranslateRequest(context.Background(), FormatOpenAI, FormatClaude, RequestEnvelope{Body: []byte(`{"messages":[]}`)})
	if !errors.Is(err, want) {
		t.Fatalf("translation error = %v, want %v", err, want)
	}
}

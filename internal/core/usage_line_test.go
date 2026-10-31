package core

import (
	"context"
	"io"
	"lmtools/internal/apifixtures"
	"net/http"
	"strings"
	"testing"
)

// usageReporter takes a response's usage the way the run's notifier does.
type usageReporter struct {
	MockNotifier
	reported []*Usage
}

func (r *usageReporter) ReportUsage(usage *Usage, _ bool) { r.reported = append(r.reported, usage) }

// A provider that refuses a prompt, as Gemini's safety filter did through
// Argo, reports every count as zero. The response's error says what
// happened, and no line of zero counts goes before it; a response with any
// count still gets its line.
func TestTokenLineLeavesOutAResponseThatCountsNothing(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}
	reply, err := apifixtures.ReadCaseFile(suite.Root, "response-argo-openai-content-filter", "captures/argo-openai.response.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg := newStreamTestRequestConfig("openai", false, false, "")
	notifier := &MockNotifier{}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(reply)))}
	response, err := HandleResponseWithOptions(context.Background(), cfg, resp, &MockLogger{logDir: t.TempDir()}, notifier, ResponseParseOptions{Output: textResponseOutput{out: &strings.Builder{}}})
	if err != nil {
		t.Fatalf("HandleResponseWithOptions() error = %v", err)
	}
	if response.Usage == nil {
		t.Fatal("the reply's usage was not read")
	}
	if err := ValidateResponse(cfg, response); err == nil || !strings.Contains(err.Error(), "content filter") {
		t.Fatalf("ValidateResponse() error = %v, want the content filter named", err)
	}
	for _, info := range notifier.infos {
		if strings.HasPrefix(info, "Tokens:") {
			t.Fatalf("notes = %q, want no token line for a reply that counts nothing", notifier.infos)
		}
	}

	zero, five := 0, 5
	counted := &Usage{InputTokens: &five, OutputTokens: &zero, TotalTokens: &five}
	for _, tt := range []struct {
		name  string
		usage *Usage
		want  *Usage
	}{
		{"every count zero", &Usage{InputTokens: &zero, OutputTokens: &zero, TotalTokens: &zero, ReasoningTokens: &zero}, nil},
		{"one count above zero", counted, counted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plain := &MockNotifier{}
			notifyTokenUsage(plain, tt.usage, false)
			if printed := len(plain.infos) > 0; printed != (tt.want != nil) {
				t.Fatalf("notes = %q, want a token line %v", plain.infos, tt.want != nil)
			}
			reporter := &usageReporter{}
			notifyTokenUsage(reporter, tt.usage, false)
			if len(reporter.reported) != 1 || reporter.reported[0] != tt.want {
				t.Fatalf("reported = %v, want one report of %v", reporter.reported, tt.want)
			}
		})
	}
}

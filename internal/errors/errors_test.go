package errors

import "testing"

func TestHTTPErrorSummarizesProviderErrorBodies(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
	}{
		{
			name: "anthropic",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Model 'x' is not available."}}`,
			want: "HTTP 400: invalid_request_error: Model 'x' is not available.",
		},
		{
			name: "openai",
			body: `{"error":{"message":"Unsupported value: 'temperature'","type":"invalid_request_error","param":"temperature","code":"unsupported_value"}}`,
			want: "HTTP 400: invalid_request_error: Unsupported value: 'temperature'",
		},
		{
			name: "openai code only",
			body: `{"error":{"message":"Rate limit reached","code":"rate_limit_exceeded"}}`,
			want: "HTTP 400: rate_limit_exceeded: Rate limit reached",
		},
		{
			name: "google",
			body: `{"error":{"code":400,"message":"API key not valid.","status":"INVALID_ARGUMENT"}}`,
			want: "HTTP 400: INVALID_ARGUMENT: API key not valid.",
		},
		{name: "bare error string", body: `{"error":"no such model"}`, want: "HTTP 400: no such model"},
		{name: "detail", body: `{"detail":"Not authenticated"}`, want: "HTTP 400: Not authenticated"},
		{name: "message", body: `{"message":"Internal error"}`, want: "HTTP 400: Internal error"},
		{name: "plain text", body: "upstream timed out\n", want: "HTTP 400: upstream timed out"},
		{name: "unknown json", body: `{"detail":[{"loc":["body"],"msg":"field required"}]}`, want: `HTTP 400: {"detail":[{"loc":["body"],"msg":"field required"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewHTTPError(400, tt.body).Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

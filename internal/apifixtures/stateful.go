package apifixtures

type StatefulScenario struct {
	Provider string         `json:"provider,omitempty"`
	Model    string         `json:"model,omitempty"`
	Steps    []StatefulStep `json:"steps"`
}

type StatefulStep struct {
	ID               string                           `json:"id"`
	Method           string                           `json:"method"`
	Path             string                           `json:"path"`
	Body             interface{}                      `json:"body,omitempty"`
	Upstream         *StatefulUpstream                `json:"upstream,omitempty"`
	Expect           StatefulExpect                   `json:"expect,omitempty"`
	Bind             map[string]string                `json:"bind,omitempty"`
	CaptureBind      map[string]string                `json:"capture_bind,omitempty"`
	PollUntil        map[string]interface{}           `json:"poll_until,omitempty"`
	CapturePollUntil map[string]interface{}           `json:"capture_poll_until,omitempty"`
	BackendRequests  []StatefulExpectedBackendRequest `json:"backend_requests,omitempty"`
}

type StatefulUpstream struct {
	Status  int    `json:"status,omitempty"`
	Body    string `json:"body,omitempty"`
	DelayMS int    `json:"delay_ms,omitempty"`
}

type StatefulExpect struct {
	Status       int                    `json:"status,omitempty"`
	JSONFields   map[string]interface{} `json:"json_fields,omitempty"`
	BodyContains []string               `json:"body_contains,omitempty"`
}

type StatefulExpectedBackendRequest struct {
	Path            string                 `json:"path,omitempty"`
	Method          string                 `json:"method,omitempty"`
	BodyContains    []string               `json:"body_contains,omitempty"`
	BodyNotContains []string               `json:"body_not_contains,omitempty"`
	JSONFields      map[string]interface{} `json:"json_fields,omitempty"`
}

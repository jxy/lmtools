package apifixtures

import "fmt"

// CaptureTarget describes a live capture destination. Provider is the wire
// format used for request rendering and response parsing; Host is the service
// that receives the request.
type CaptureTarget struct {
	ID       string
	Provider string
	Host     string
	Stream   bool
}

// ParseCaptureTarget decodes supported live-capture target ids.
func ParseCaptureTarget(targetID string) (CaptureTarget, error) {
	switch targetID {
	case "openai":
		return CaptureTarget{ID: targetID, Provider: "openai", Host: "openai"}, nil
	case "openai-stream":
		return CaptureTarget{ID: targetID, Provider: "openai", Host: "openai", Stream: true}, nil
	case "openai-responses":
		return CaptureTarget{ID: targetID, Provider: "openai-responses", Host: "openai"}, nil
	case "openai-responses-stream":
		return CaptureTarget{ID: targetID, Provider: "openai-responses", Host: "openai", Stream: true}, nil
	case "anthropic":
		return CaptureTarget{ID: targetID, Provider: "anthropic", Host: "anthropic"}, nil
	case "anthropic-stream":
		return CaptureTarget{ID: targetID, Provider: "anthropic", Host: "anthropic", Stream: true}, nil
	case "google":
		return CaptureTarget{ID: targetID, Provider: "google", Host: "google"}, nil
	case "google-stream":
		return CaptureTarget{ID: targetID, Provider: "google", Host: "google", Stream: true}, nil
	case "argo":
		return CaptureTarget{ID: targetID, Provider: "argo", Host: "argo"}, nil
	case "argo-stream":
		return CaptureTarget{ID: targetID, Provider: "argo", Host: "argo", Stream: true}, nil
	case "argo-openai":
		return CaptureTarget{ID: targetID, Provider: "openai", Host: "argo"}, nil
	case "argo-openai-stream":
		return CaptureTarget{ID: targetID, Provider: "openai", Host: "argo", Stream: true}, nil
	case "argo-anthropic":
		return CaptureTarget{ID: targetID, Provider: "anthropic", Host: "argo"}, nil
	case "argo-anthropic-stream":
		return CaptureTarget{ID: targetID, Provider: "anthropic", Host: "argo", Stream: true}, nil
	default:
		return CaptureTarget{}, fmt.Errorf("unsupported target %q", targetID)
	}
}

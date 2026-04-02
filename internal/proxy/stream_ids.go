package proxy

func newStreamID(prefix string) string {
	return generateUUID(prefix)
}

func newAnthropicStreamID() string {
	return newStreamID("msg_")
}

func newOpenAIStreamID() string {
	return newStreamID("chatcmpl-")
}

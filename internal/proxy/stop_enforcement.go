package proxy

import (
	"context"
	"lmtools/internal/logger"
	"unicode/utf8"
)

func nonEmptyStopSequences(stops []string) []string {
	if len(stops) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(stops))
	for _, stop := range stops {
		if stop != "" {
			filtered = append(filtered, stop)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func stripOpenAICompatibleStop(req *OpenAIRequest) []string {
	if req == nil {
		return nil
	}
	stops := nonEmptyStopSequences([]string(req.Stop))
	req.Stop = nil
	return stops
}

func warnOpenAICompatibleStopSpecialProcessing(ctx context.Context, requestName string, stops []string) {
	filtered := nonEmptyStopSequences(stops)
	if len(filtered) == 0 {
		return
	}
	logger.From(ctx).Warnf("OpenAI-compatible stop special processing for %s: %d stop sequence(s) stripped from upstream request and enforced locally; DEBUG wire logs remain raw at each client/backend boundary", requestName, len(filtered))
}

func truncateAtFirstStop(text string, stops []string) (string, bool) {
	stops = nonEmptyStopSequences(stops)
	if text == "" || len(stops) == 0 {
		return text, false
	}
	textRunes := []rune(text)
	best := -1
	for _, stop := range stops {
		idx := indexRunes(textRunes, []rune(stop))
		if idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	if best < 0 {
		return text, false
	}
	return string(textRunes[:best]), true
}

type stopTextEnforcer struct {
	stops      [][]rune
	pending    []rune
	maxStopLen int
	stopped    bool
}

func newStopTextEnforcer(stops []string) *stopTextEnforcer {
	filtered := nonEmptyStopSequences(stops)
	if len(filtered) == 0 {
		return nil
	}
	enforcer := &stopTextEnforcer{stops: make([][]rune, 0, len(filtered))}
	for _, stop := range filtered {
		runes := []rune(stop)
		if len(runes) == 0 {
			continue
		}
		enforcer.stops = append(enforcer.stops, runes)
		if len(runes) > enforcer.maxStopLen {
			enforcer.maxStopLen = len(runes)
		}
	}
	if len(enforcer.stops) == 0 {
		return nil
	}
	return enforcer
}

func (e *stopTextEnforcer) Push(text string) (string, bool) {
	if e == nil {
		return text, false
	}
	if e.stopped || text == "" && len(e.pending) == 0 {
		return "", e.stopped
	}
	if text != "" {
		e.pending = append(e.pending, safeRunes(text)...)
	}

	if idx := e.firstStopIndex(e.pending); idx >= 0 {
		emit := string(e.pending[:idx])
		e.pending = nil
		e.stopped = true
		return emit, true
	}

	hold := e.maxStopLen - 1
	if hold < 0 {
		hold = 0
	}
	if len(e.pending) <= hold {
		return "", false
	}
	emitRunes := e.pending[:len(e.pending)-hold]
	e.pending = append([]rune(nil), e.pending[len(e.pending)-hold:]...)
	return string(emitRunes), false
}

func (e *stopTextEnforcer) Flush() string {
	if e == nil || e.stopped || len(e.pending) == 0 {
		return ""
	}
	emit := string(e.pending)
	e.pending = nil
	return emit
}

func (e *stopTextEnforcer) Stopped() bool {
	return e != nil && e.stopped
}

func (e *stopTextEnforcer) firstStopIndex(text []rune) int {
	best := -1
	for _, stop := range e.stops {
		idx := indexRunes(text, stop)
		if idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	return best
}

func indexRunes(text, stop []rune) int {
	if len(stop) == 0 || len(stop) > len(text) {
		return -1
	}
	for i := 0; i <= len(text)-len(stop); i++ {
		matched := true
		for j := range stop {
			if text[i+j] != stop[j] {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

func safeRunes(text string) []rune {
	if utf8.ValidString(text) {
		return []rune(text)
	}
	return []rune(string([]byte(text)))
}

func enforceOpenAIResponseStops(resp *OpenAIResponse, stops []string) bool {
	stops = nonEmptyStopSequences(stops)
	if resp == nil || len(stops) == 0 {
		return false
	}
	matchedAny := false
	for i := range resp.Choices {
		content, ok := resp.Choices[i].Message.Content.(string)
		if !ok || content == "" {
			continue
		}
		truncated, matched := truncateAtFirstStop(content, stops)
		if matched {
			resp.Choices[i].Message.Content = truncated
			resp.Choices[i].FinishReason = "stop"
			matchedAny = true
		}
	}
	return matchedAny
}

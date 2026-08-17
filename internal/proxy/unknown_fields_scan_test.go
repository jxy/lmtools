package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// buildTranscriptRequest returns a chat request with turns messages. Each
// message carries extra keys drawn from extraKeys, so the caller controls
// whether the unknown paths in the document repeat or are all distinct.
func buildTranscriptRequest(turns int, extraKeys func(turn int) string) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5","messages":[`)
	for i := 0; i < turns; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"user","content":"step %d: inspect the build output and continue"`, i)
		if extra := extraKeys(i); extra != "" {
			b.WriteByte(',')
			b.WriteString(extra)
		}
		b.WriteByte('}')
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func scanRequestPaths(t *testing.T, data []byte) ([]string, bool) {
	t.Helper()
	paths, truncated, err := scanUnknownFieldPaths(data, reflect.TypeOf(OpenAIRequest{}))
	if err != nil {
		t.Fatalf("scanUnknownFieldPaths() error = %v", err)
	}
	return paths, truncated
}

// scanAllocations reports allocations for one scan after warming the global
// reflection cache. Comparing a one-element document with a long repetition of
// the same shape catches per-occurrence work without tying the test to object
// sizes or allocator accounting on one Go release.
func scanAllocations(t *testing.T, body []byte) float64 {
	t.Helper()
	targetType := reflect.TypeOf(OpenAIRequest{})
	scanRequestPaths(t, []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))

	var paths []string
	var truncated bool
	var scanErr error
	allocs := testing.AllocsPerRun(5, func() {
		paths, truncated, scanErr = scanUnknownFieldPaths(body, targetType)
	})
	if scanErr != nil {
		t.Fatalf("scanUnknownFieldPaths() error = %v", scanErr)
	}
	runtime.KeepAlive(paths)
	runtime.KeepAlive(truncated)
	return allocs
}

// TestScanUnknownFieldPathsDeduplicatesRepeatedPaths pins the ordinary case:
// one unknown key on every message of a long transcript is one unknown path,
// not one per message.
func TestScanUnknownFieldPathsDeduplicatesRepeatedPaths(t *testing.T) {
	const turns = 20000
	body := buildTranscriptRequest(turns, func(int) string { return `"clientHint":1` })

	paths, truncated := scanRequestPaths(t, body)
	if truncated {
		t.Error("truncated = true, want false: the document has one distinct unknown path")
	}
	want := []string{"messages[].clientHint"}
	if !reflect.DeepEqual(paths, want) {
		if len(paths) > 8 {
			t.Fatalf("scanUnknownFieldPaths() returned %d paths for %d repeats of one key, want %v", len(paths), turns, want)
		}
		t.Fatalf("scanUnknownFieldPaths() = %v, want %v", paths, want)
	}
}

// Known scalar and interface fields do not need a diagnostic path, and a long
// array visits the same structural paths in every element. Neither should
// allocate a newly joined path on every visit.
func TestScanUnknownFieldPathsReusesKnownPathsAcrossArrayElements(t *testing.T) {
	shortBody := buildTranscriptRequest(1, func(int) string { return "" })
	longBody := buildTranscriptRequest(20000, func(int) string { return "" })

	shortAllocs := scanAllocations(t, shortBody)
	longAllocs := scanAllocations(t, longBody)
	if longAllocs > shortAllocs+8 {
		t.Fatalf("19999 repeated known objects added %.1f allocations (%.1f total versus %.1f for one), want at most 8",
			longAllocs-shortAllocs, longAllocs, shortAllocs)
	}
}

// Escaped names have to be decoded to match struct tags and to report the
// spelling the client meant. Repeating one such unknown key must not repeat the
// decoder's temporary allocations before path deduplication can see it.
func TestScanUnknownFieldPathsReusesEscapedKeysAcrossArrayElements(t *testing.T) {
	const escapedUnknown = `"\u0078":1`
	shortBody := buildTranscriptRequest(1, func(int) string { return escapedUnknown })
	longBody := buildTranscriptRequest(20000, func(int) string { return escapedUnknown })

	paths, truncated := scanRequestPaths(t, longBody)
	if truncated {
		t.Error("truncated = true, want false for one repeated escaped path")
	}
	if want := []string{"messages[].x"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("scanUnknownFieldPaths() = %q, want %q", paths, want)
	}

	shortAllocs := scanAllocations(t, shortBody)
	longAllocs := scanAllocations(t, longBody)
	if longAllocs > shortAllocs+8 {
		t.Fatalf("19999 repeated escaped keys added %.1f allocations (%.1f total versus %.1f for one), want at most 8",
			longAllocs-shortAllocs, longAllocs, shortAllocs)
	}
}

// encoding/json accepts malformed UTF-8 in a string and replaces it with
// RuneError. Unknown-field diagnostics must use that same spelling without
// allocating a replacement string for every repeated occurrence.
func TestScanUnknownFieldPathsReusesInvalidUTF8KeysAcrossArrayElements(t *testing.T) {
	invalidUnknown := "\"bad" + string([]byte{0xff}) + "\":1"
	shortBody := buildTranscriptRequest(1, func(int) string { return invalidUnknown })
	longBody := buildTranscriptRequest(20000, func(int) string { return invalidUnknown })

	paths, truncated := scanRequestPaths(t, longBody)
	if truncated {
		t.Error("truncated = true, want false for one repeated invalid UTF-8 path")
	}
	if want := []string{"messages[].bad\ufffd"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("scanUnknownFieldPaths() = %q, want %q", paths, want)
	}

	shortAllocs := scanAllocations(t, shortBody)
	longAllocs := scanAllocations(t, longBody)
	if longAllocs > shortAllocs+8 {
		t.Fatalf("19999 repeated invalid UTF-8 keys added %.1f allocations (%.1f total versus %.1f for one), want at most 8",
			longAllocs-shortAllocs, longAllocs, shortAllocs)
	}
}

// The reusable decoder replaces encoding/json on a memory-sensitive path, so
// pin its behavior for every JSON escape class, surrogate pairs, replacement
// of invalid UTF-8, and lone surrogates accepted by encoding/json.
func TestDecodeJSONKeyMatchesEncodingJSON(t *testing.T) {
	cases := []struct {
		name    string
		encoded []byte
	}{
		{"plain UTF-8", []byte("café")},
		{"punctuation escapes", []byte(`quote\" slash\/ backslash\\`)},
		{"control escapes", []byte(`\b\f\n\r\t`)},
		{"unicode escape", []byte(`caf\u00e9`)},
		{"surrogate pair", []byte(`face\ud83d\ude00`)},
		{"lone high surrogate", []byte(`bad\ud800x`)},
		{"lone low surrogate", []byte(`bad\udc00x`)},
		{"invalid UTF-8", append([]byte("bad"), 0xff)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quoted := make([]byte, 0, len(tc.encoded)+2)
			quoted = append(quoted, '"')
			quoted = append(quoted, tc.encoded...)
			quoted = append(quoted, '"')

			var want string
			if err := json.Unmarshal(quoted, &want); err != nil {
				t.Fatalf("encoding/json rejected test input %q: %v", quoted, err)
			}
			scanner := &jsonStructScanner{}
			got, err := scanner.decodeJSONKey(tc.encoded)
			if err != nil {
				t.Fatalf("decodeJSONKey() error = %v", err)
			}
			if string(got) != want {
				t.Fatalf("decodeJSONKey(%q) = %q, want %q", tc.encoded, got, want)
			}
		})
	}
}

func TestDecodeJSONKeyRejectsInvalidEscapes(t *testing.T) {
	for _, encoded := range [][]byte{[]byte(`\x`), []byte(`\u12`), []byte("raw\ncontrol")} {
		scanner := &jsonStructScanner{}
		if _, err := scanner.decodeJSONKey(encoded); err == nil {
			t.Errorf("decodeJSONKey(%q) error = nil, want invalid-key error", encoded)
		}
	}
}

func TestScanUnknownFieldPathsRejectsMalformedSkippedValues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"short true", `{"clientHint":tru}`},
		{"extended true", `{"clientHint":truth}`},
		{"short false", `{"clientHint":fals}`},
		{"short null", `{"clientHint":nul}`},
		{"leading zero", `{"clientHint":01}`},
		{"missing integer", `{"clientHint":.5}`},
		{"missing fraction", `{"clientHint":1.}`},
		{"missing exponent", `{"clientHint":1e+}`},
		{"plus sign", `{"clientHint":+1}`},
		{"arbitrary token", `{"clientHint":wat}`},
		{"malformed nested literal", `{"clientHint":{"nested":tru}}`},
		{"missing nested comma", `{"clientHint":[1 2]}`},
		{"mismatched nested delimiter", `{"clientHint":{"nested":1]}`},
		{"invalid string escape", `{"clientHint":"bad\x"}`},
		{"raw string control", "{\"clientHint\":\"line\nbreak\"}"},
	}

	targetType := reflect.TypeOf(OpenAIRequest{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			if json.Valid(body) {
				t.Fatalf("test body unexpectedly valid JSON: %s", body)
			}
			paths, truncated, err := scanUnknownFieldPaths(body, targetType)
			if err == nil {
				t.Fatalf("scanUnknownFieldPaths(%s) = (%v, %v, nil), want malformed-JSON error", body, paths, truncated)
			}
		})
	}
}

func TestScanUnknownFieldPathsAcceptsValidSkippedValues(t *testing.T) {
	values := []string{
		`true`,
		`false`,
		`null`,
		`0`,
		`-0`,
		`1234567890`,
		`-12.34`,
		`1e9`,
		`-1.2E-3`,
		`{"nested":[true,false,null,0,-2.5e+4,"valid\\nescape"]}`,
	}

	for _, value := range values {
		body := []byte(`{"clientHint":` + value + `}`)
		if !json.Valid(body) {
			t.Fatalf("test body unexpectedly invalid JSON: %s", body)
		}
		paths, truncated := scanRequestPaths(t, body)
		if truncated {
			t.Errorf("scanUnknownFieldPaths(%s) truncated = true, want false", body)
		}
		if want := []string{"clientHint"}; !reflect.DeepEqual(paths, want) {
			t.Errorf("scanUnknownFieldPaths(%s) = %v, want %v", body, paths, want)
		}
	}
}

// Unknown-field detection runs before encoding/json decodes the request. It
// must therefore reject the same excessive nesting before its recursive walk
// can keep growing the goroutine stack. Responses input is intentionally an
// interface field, so nested containers take the scanner's skipped-value path.
func TestScanUnknownFieldPathsEnforcesEncodingJSONNestingLimit(t *testing.T) {
	const encodingJSONMaxNestingDepth = 10000
	nestedResponsesInput := func(depth int, open, close string) []byte {
		var body strings.Builder
		body.Grow(len(`{"model":"gpt-5","input":}`) + depth*(len(open)+len(close)) + len("null"))
		body.WriteString(`{"model":"gpt-5","input":`)
		body.WriteString(strings.Repeat(open, depth))
		body.WriteString("null")
		body.WriteString(strings.Repeat(close, depth))
		body.WriteByte('}')
		return []byte(body.String())
	}

	targetType := reflect.TypeOf(OpenAIResponsesRequest{})
	for _, tc := range []struct {
		name        string
		open, close string
	}{
		{name: "skipped arrays", open: "[", close: "]"},
		{name: "skipped objects", open: `{"nested":`, close: "}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The top-level request object consumes one container level, leaving
			// the rest of encoding/json's limit available to input.
			atLimit := nestedResponsesInput(encodingJSONMaxNestingDepth-1, tc.open, tc.close)
			if !json.Valid(atLimit) {
				t.Fatal("encoding/json rejected a request at its documented nesting limit")
			}
			if paths, truncated, err := scanUnknownFieldPaths(atLimit, targetType); err != nil {
				t.Fatalf("scanUnknownFieldPaths() rejected nesting accepted by encoding/json: %v", err)
			} else if len(paths) != 0 || truncated {
				t.Fatalf("scanUnknownFieldPaths() = (%v, %v), want no unknown paths", paths, truncated)
			}

			overLimit := nestedResponsesInput(encodingJSONMaxNestingDepth, tc.open, tc.close)
			if json.Valid(overLimit) {
				t.Fatal("encoding/json accepted a request beyond its nesting limit")
			}
			var decoded OpenAIResponsesRequest
			if err := json.Unmarshal(overLimit, &decoded); err == nil || !strings.Contains(err.Error(), "exceeded max depth") {
				t.Fatalf("json.Unmarshal() error = %v, want exceeded-max-depth error", err)
			}
			if _, _, err := scanUnknownFieldPaths(overLimit, targetType); err == nil || !strings.Contains(err.Error(), "exceeded max depth") {
				t.Fatalf("scanUnknownFieldPaths() error = %v, want exceeded-max-depth error", err)
			}
		})
	}
}

// Escaped known parents must contribute their decoded, canonical spelling to
// descendant paths; the path cache must never retain request-encoded segments.
func TestScanUnknownFieldPathsUsesCanonicalEscapedParentPaths(t *testing.T) {
	body := []byte(`{"\u006dessages":[{"\u0072ole":"user","\u0078":1}]}`)
	paths, truncated := scanRequestPaths(t, body)
	if truncated {
		t.Error("truncated = true, want false")
	}
	if want := []string{"messages[].x"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("scanUnknownFieldPaths() = %q, want %q", paths, want)
	}
}

// TestScanUnknownFieldPathsCapsDistinctPaths pins the adversarial case: a body
// whose every key is different cannot size the scanner's memory, or the log
// line's, off the request.
func TestScanUnknownFieldPathsCapsDistinctPaths(t *testing.T) {
	const turns = 5000
	body := buildTranscriptRequest(turns, func(turn int) string {
		return fmt.Sprintf(`"hint%d":1`, turn)
	})

	paths, truncated := scanRequestPaths(t, body)
	if len(paths) != maxUnknownFieldPaths {
		t.Fatalf("scanUnknownFieldPaths() returned %d paths for %d distinct unknown keys, want the cap of %d",
			len(paths), turns, maxUnknownFieldPaths)
	}
	if !truncated {
		t.Error("truncated = false, want true once the cap is reached")
	}
}

// Once the scanner has said the list is truncated, later keys cannot change
// the diagnostic. In particular, they must not each buy a bounded string copy:
// millions of occurrences after the cap would make allocation and GC scale
// with the payload the cap was meant to detach from memory use.
func TestScanUnknownFieldPathsStopsAllocatingAfterTheCap(t *testing.T) {
	scanner := &jsonStructScanner{
		paths:     make([]string, maxUnknownFieldPaths),
		truncated: true,
	}
	key := []byte(strings.Repeat("k", maxUnknownFieldKeyBytes+1))

	if got := testing.AllocsPerRun(1000, func() {
		scanner.noteUnknownPath("messages[]", key)
	}); got != 0 {
		t.Fatalf("noteUnknownPath() allocated %.1f times after truncation, want 0", got)
	}
}

// Escaped keys are decoded before noteUnknownPath sees them, so the guard above
// also has to exist at the decoding boundary. Otherwise every short escaped
// key after saturation allocates even though none can affect the diagnostic.
func TestScanUnknownFieldPathsStopsDecodingEscapedKeysAfterTheCap(t *testing.T) {
	buildBody := func(escapedKeys int) []byte {
		var b strings.Builder
		b.WriteString(`{"model":"gpt-5","messages":[]`)
		// The first maxUnknownFieldPaths keys fill the result; the next distinct
		// key marks it truncated before the escaped-key suffix is scanned.
		for i := 0; i <= maxUnknownFieldPaths; i++ {
			fmt.Fprintf(&b, `,"hint%d":1`, i)
		}
		for i := 0; i < escapedKeys; i++ {
			b.WriteString(`,"\u0061":1`)
		}
		b.WriteByte('}')
		return []byte(b.String())
	}

	baseline := buildBody(0)
	withEscapedSuffix := buildBody(5000)
	targetType := reflect.TypeOf(OpenAIRequest{})
	// Warm the per-type reflection cache before measuring scanner allocations.
	scanRequestPaths(t, baseline)

	allocs := func(body []byte) float64 {
		var scanErr error
		got := testing.AllocsPerRun(10, func() {
			_, _, scanErr = scanUnknownFieldPaths(body, targetType)
		})
		if scanErr != nil {
			t.Fatalf("scanUnknownFieldPaths() error = %v", scanErr)
		}
		return got
	}
	baselineAllocs := allocs(baseline)
	escapedAllocs := allocs(withEscapedSuffix)
	if escapedAllocs > baselineAllocs+2 {
		t.Fatalf("5000 escaped keys after truncation added %.1f allocations (%.1f total versus %.1f baseline), want at most 2",
			escapedAllocs-baselineAllocs, escapedAllocs, baselineAllocs)
	}
}

// TestWarnUnknownFieldsSaysWhenTheListWasCut keeps the log line honest: a
// reader must be able to tell a complete list from the first sixty-four of
// many.
func TestWarnUnknownFieldsSaysWhenTheListWasCut(t *testing.T) {
	body := buildTranscriptRequest(200, func(turn int) string {
		return fmt.Sprintf(`"hint%d":1`, turn)
	})

	logs := captureWarnLogs(t, func() {
		warnUnknownFields(context.Background(), body, OpenAIRequest{}, "client request")
	})
	if !strings.Contains(logs, ", and more") {
		t.Fatalf("truncated warning does not say the list was cut:\n%s", logs)
	}

	shortLogs := captureWarnLogs(t, func() {
		warnUnknownFields(context.Background(), []byte(`{"model":"gpt-5","messages":[],"clientHint":1}`), OpenAIRequest{}, "client request")
	})
	if !strings.Contains(shortLogs, "clientHint") {
		t.Fatalf("warning missing the unknown field:\n%s", shortLogs)
	}
	if strings.Contains(shortLogs, ", and more") {
		t.Fatalf("a complete list claims to be cut short:\n%s", shortLogs)
	}
}

// The path cap bounds the diagnostic only if each path is bounded too, and the
// key half of a path is written by the client. A key that is a large fraction
// of the body used to be copied twice on the way to being kept whole, so
// sixty-four of them let a request buy multiples of itself in log state before
// anything was decoded.
func TestScanUnknownFieldPathsBoundsOversizedKeys(t *testing.T) {
	const keyBytes = 8 << 20
	key := strings.Repeat("k", keyBytes)
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","` + key + `":1}]}`)

	// Reading the key in place and copying only what is kept leaves the scan
	// costing a fixed handful of kilobytes, against three copies of the key
	// before.
	const budget = 0.01
	if got := scanAllocationMultiple(t, body); got > budget {
		t.Errorf("scanning a body of one %d byte key allocated %.3fx the body, want at most %.3fx", keyBytes, got, budget)
	}

	paths, _ := scanRequestPaths(t, body)
	if len(paths) != 1 {
		t.Fatalf("scanUnknownFieldPaths() = %v, want one path", paths)
	}
	if len(paths[0]) > len("messages[].")+maxUnknownFieldKeyBytes+len(unknownFieldKeyEllipsis) {
		t.Fatalf("reported path is %d bytes, want it bounded by the %d byte key cap", len(paths[0]), maxUnknownFieldKeyBytes)
	}
	if !strings.HasPrefix(paths[0], "messages[].kkk") {
		t.Fatalf("path = %q, want the start of the key so it can still be identified", paths[0])
	}
	if !strings.HasSuffix(paths[0], unknownFieldKeyEllipsis) {
		t.Fatalf("path = %q, want it to say the name was shortened", paths[0])
	}
}

// jsonEscapedKey spells every character of name as a \uXXXX escape, the
// longest legal way to write it. JSON permits it and the field is still the
// field, so nothing about the encoding may change whether the scanner
// recognizes the name.
func jsonEscapedKey(name string) string {
	var escaped strings.Builder
	for _, r := range name {
		fmt.Fprintf(&escaped, `\u%04x`, r)
	}
	return escaped.String()
}

// A field is known by the name it spells, not by how many bytes the client
// spent spelling it. Six bytes per character puts every longer field name in
// this proxy past a cap meant for what a key decodes to, and the scanner
// answered by skipping the decode and warning that a supported field was
// ignored.
func TestScanUnknownFieldPathsRecognizesFullyEscapedKnownKeys(t *testing.T) {
	// Long enough that the escaped form is over maxUnknownFieldKeyBytes: 22
	// characters at six bytes each is 132.
	const known = "prompt_cache_retention"
	escaped := jsonEscapedKey(known)
	if len(escaped) <= maxUnknownFieldKeyBytes {
		t.Fatalf("%q escapes to %d bytes, want more than the %d byte cap for this to test anything", known, len(escaped), maxUnknownFieldKeyBytes)
	}
	if _, ok := getStructJSONFieldTypes(reflect.TypeOf(OpenAIRequest{}))[known]; !ok {
		t.Fatalf("OpenAIRequest has no %q field; pick another long known name", known)
	}

	if paths, _ := scanRequestPaths(t, []byte(`{"model":"gpt-5","`+escaped+`":"x"}`)); len(paths) != 0 {
		t.Fatalf("scanUnknownFieldPaths() = %q, want nothing: %s is a known field however it is spelled", paths, known)
	}

	// The gate is the longest a keepable name can be written, so a key past it
	// is still refused without being decoded, and an unknown one still reports.
	overGate := strings.Repeat("k", maxUnknownFieldKeyBytes*maxJSONEscapeBytes) + `k`
	paths, _ := scanRequestPaths(t, []byte(`{"model":"gpt-5","`+overGate+`":1}`))
	if len(paths) != 1 || !strings.HasSuffix(paths[0], unknownFieldKeyEllipsis) {
		t.Fatalf("scanUnknownFieldPaths() = %q, want one shortened unknown path", paths)
	}
}

// A short key is reported exactly as sent, escapes decoded, whatever its bytes.
func TestScanUnknownFieldPathsKeepsOrdinaryKeysIntact(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"plain", "clientHint", "clientHint"},
		// A \u escape in the key still has to match the name it spells.
		{"escaped", "client\\u0048int", "clientHint"},
		// Including when every character is one, which is six times the bytes for
		// the same name.
		{"fully escaped", jsonEscapedKey("clientHintWithAQuiteLongName"), "clientHintWithAQuiteLongName"},
		{"at the cap", strings.Repeat("k", maxUnknownFieldKeyBytes), strings.Repeat("k", maxUnknownFieldKeyBytes)},
		{"one past the cap", strings.Repeat("k", maxUnknownFieldKeyBytes+1), strings.Repeat("k", maxUnknownFieldKeyBytes) + unknownFieldKeyEllipsis},
		// Cutting mid-rune would put half a character in the log line.
		{"multibyte across the cut", strings.Repeat("k", maxUnknownFieldKeyBytes-1) + "éx", strings.Repeat("k", maxUnknownFieldKeyBytes-1) + unknownFieldKeyEllipsis},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths, _ := scanRequestPaths(t, []byte(`{"model":"gpt-5","`+tc.key+`":1}`))
			if !reflect.DeepEqual(paths, []string{tc.want}) {
				t.Fatalf("scanUnknownFieldPaths() = %q, want %q", paths, []string{tc.want})
			}
		})
	}
}

// scanAllocationMultiple reports what one scan of body allocated, as a multiple
// of the body's own size. AGENTS.md requires the scanner to stay a small
// multiple of the payload, so this is the number the invariant is written in.
func scanAllocationMultiple(t *testing.T, body []byte) float64 {
	t.Helper()
	// Warm the per-type field cache so first-use initialization is not charged
	// to the measurement.
	scanRequestPaths(t, []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	scanRequestPaths(t, body)
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(len(body))
}

// TestScanUnknownFieldPathsStaysProportionalToTheBody covers the shapes that
// used to scale differently: a clean document, one plain or escaped unknown key
// repeated across a long array, and a document of distinct unknown keys.
func TestScanUnknownFieldPathsStaysProportionalToTheBody(t *testing.T) {
	cases := []struct {
		name  string
		extra func(turn int) string
	}{
		{"no unknown fields", func(int) string { return "" }},
		{"one unknown key repeated", func(int) string { return `"clientHint":1` }},
		{"one escaped unknown key repeated", func(int) string { return `"\u0078":1` }},
		{"all keys distinct", func(turn int) string { return fmt.Sprintf(`"hint%d":1`, turn) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildTranscriptRequest(30000, tc.extra)
			// The scan materializes object keys and skips values, so its cost is
			// well under the body. The bound is loose enough not to depend on the
			// exact key sizes and tight enough to fail if the scan starts keeping
			// per-occurrence state again.
			const budget = 0.25
			if got := scanAllocationMultiple(t, body); got > budget {
				t.Errorf("scanning a %d byte body allocated %.1fx the body, want at most %.1fx", len(body), got, budget)
			}
		})
	}
}

func FuzzScanUnknownFieldPathsMatchesJSONValidity(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`null`),
		[]byte(`{"known":"value"}`),
		[]byte(`{"known":"value","future":{"items":[1,true,null]}}`),
		[]byte(`{"\u006bnown":"escaped"}`),
		[]byte(`{"broken":`),
		[]byte(`[1,2,]`),
	} {
		f.Add(seed)
	}
	targetType := reflect.TypeOf(struct {
		Known string `json:"known"`
	}{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, err := scanUnknownFieldPaths(data, targetType)
		if valid := json.Valid(data); valid != (err == nil) {
			t.Fatalf("json.Valid = %t, scan error = %v, input = %q", valid, err, data)
		}
	})
}

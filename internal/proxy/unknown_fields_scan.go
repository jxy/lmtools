package proxy

// Structural JSON Scanning For Unknown-Field Detection
//
// Unknown-field warnings only need the shape of a document: which keys appear,
// and where. Decoding the body into interface{} to find that out costs several
// times the body size in maps, interface boxes, and copies of every string —
// on every request, purely to produce log output.
//
// This scanner walks the raw bytes instead. It skips values by scanning past
// them and reads object keys in place, materializing one only where the answer
// has to outlive the document — and then bounded, because a key's length is the
// client's to choose. Scan time remains linear in the input, while retained
// diagnostic memory is independent of body size, so large requests keep their
// warnings without a second payload-sized representation.

import (
	"bytes"
	"fmt"
	"reflect"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// scanUnknownFieldPaths reports the JSON paths in data that have no matching
// field in targetType, and whether it stopped collecting at
// maxUnknownFieldPaths. It returns an error for malformed JSON, matching what
// the decoder-based implementation reported.
func scanUnknownFieldPaths(data []byte, targetType reflect.Type) ([]string, bool, error) {
	scanner := &jsonStructScanner{data: data}
	if err := scanner.scanValue(targetType, ""); err != nil {
		return nil, false, err
	}
	scanner.skipSpace()
	if scanner.pos != len(scanner.data) {
		return nil, false, fmt.Errorf("invalid character %q after top-level value", scanner.data[scanner.pos])
	}
	return scanner.paths, scanner.truncated, nil
}

// maxUnknownFieldPaths caps how many distinct paths one document contributes.
// The output is a log line, and a reader who has seen sixty-four unknown fields
// has the idea; past that the cap is what keeps a body full of unknown keys from
// sizing the scanner's memory, and the log line's, off the request.
const maxUnknownFieldPaths = 64

// maxJSONNestingDepth matches encoding/json's container-depth limit. Unknown
// field detection runs before decoding, so it must stop at the same boundary
// instead of recursively walking input that the decoder will reject anyway.
const maxJSONNestingDepth = 10000

// maxUnknownFieldKeyBytes bounds what any one object key contributes. Capping
// the number of paths bounds the diagnostic only if each path is itself
// bounded, and a key is the one part of a path the client writes: an 8MB name
// buys 8MB of retained path, 8MB of log line, and a copy of each on the way, so
// sixty-four of them put the payload back in charge of the diagnostic a field
// at a time. Every JSON name this proxy knows is a couple of dozen bytes, so a
// key past this is being identified in a log line, not read.
const maxUnknownFieldKeyBytes = 128

// maxJSONEscapeBytes is the most a JSON string spends to encode one decoded
// byte: six, for the \uXXXX form of an ASCII character. Nothing in the grammar
// expands — every other escape spends two bytes on one, and a surrogate pair
// twelve on four — so a key written in more than this many bytes per byte kept
// cannot decode to a name short enough to keep, and scanKey can decline to
// decode it. Measuring the encoded length against maxUnknownFieldKeyBytes
// directly is the same test with the ratio left out, and it reported
// `prompt_cache_retention` as an unknown field when a client spelled all
// twenty-two of its characters as escapes.
const maxJSONEscapeBytes = 6

// maxDecodedKeyScratchBytes bounds the reusable destination for escaped keys.
// Invalid UTF-8 is legal input to encoding/json and is replaced by the
// three-byte UTF-8 encoding of RuneError, so reserve utf8.UTFMax bytes for each
// encoded byte. Valid JSON escapes only shrink or stay well below this bound.
const maxDecodedKeyScratchBytes = maxUnknownFieldKeyBytes * maxJSONEscapeBytes * utf8.UTFMax

// unknownFieldKeyEllipsis ends a key this scanner shortened, so a reader can
// tell one from a name that really is that long.
const unknownFieldKeyEllipsis = "..."

// unknownFieldKeyPrefix selects the part of key worth keeping, cutting on a
// rune boundary so a warning never ends on half a character. Two keys sharing
// a prefix this long collapse to one path, which is the cap doing its job rather
// than a loss: they are equally unidentifiable at full length.
func unknownFieldKeyPrefix(key []byte) ([]byte, bool) {
	if len(key) <= maxUnknownFieldKeyBytes {
		return key, false
	}
	cut := key[:maxUnknownFieldKeyBytes]
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRune(cut); r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

func unknownFieldKeyText(key []byte) string {
	prefix, shortened := unknownFieldKeyPrefix(key)
	if !shortened {
		return string(prefix)
	}
	return string(prefix) + unknownFieldKeyEllipsis
}

func stringEqualsBytes(text string, data []byte) bool {
	if len(text) != len(data) {
		return false
	}
	for i := range data {
		if text[i] != data[i] {
			return false
		}
	}
	return true
}

// unknownFieldKeyMatches compares an in-place or scratch-buffer key with the
// bounded spelling retained in seen. It deliberately avoids converting key to
// a string: even a bounded conversion per repeated occurrence makes allocation
// proportional to an array's length.
func unknownFieldKeyMatches(seen string, key []byte) bool {
	prefix, shortened := unknownFieldKeyPrefix(key)
	if !shortened {
		return stringEqualsBytes(seen, prefix)
	}
	if len(seen) != len(prefix)+len(unknownFieldKeyEllipsis) {
		return false
	}
	return stringEqualsBytes(seen[:len(prefix)], prefix) && seen[len(prefix):] == unknownFieldKeyEllipsis
}

type jsonStructScanner struct {
	data  []byte
	pos   int
	paths []string
	// nestingDepth counts open JSON objects and arrays across both reflected
	// scanning and skipped values. It adds no payload-sized state: only the
	// recursive call stack grows, and that growth is capped at the same point as
	// encoding/json's parser stack.
	nestingDepth int
	// seen deduplicates as the scan runs rather than after it. Repeats are the
	// normal case, not the adversarial one: every element of a message array
	// contributes the same path, so a body with one unknown field per message
	// yielded one copy of that path per message.
	seen      []unknownPathKey
	truncated bool

	// pathCache retains one joined path per distinct structural edge. Array
	// elements revisit the same edges, so their path work is constant rather
	// than proportional to the number of elements.
	pathCache map[jsonPathCacheKey]string

	// decodedKeyScratch is reused for every escaped or invalid UTF-8 key.
	// scanKey's result is consumed before the next key is scanned, so no
	// occurrence needs its own decoded string or byte slice.
	decodedKeyScratch [maxDecodedKeyScratchBytes]byte
}

// unknownPathKey identifies a path by its parts, so testing whether a path has
// been seen does not build the joined string. Only a path that is actually kept
// is joined.
type unknownPathKey struct {
	prefix string
	key    string
}

type jsonPathCacheKey struct {
	prefix string
	field  string
	array  bool
}

func (s *jsonStructScanner) fieldPath(prefix, field string) string {
	if prefix == "" {
		return field
	}
	cacheKey := jsonPathCacheKey{prefix: prefix, field: field}
	if path, ok := s.pathCache[cacheKey]; ok {
		return path
	}
	if s.pathCache == nil {
		s.pathCache = make(map[jsonPathCacheKey]string)
	}
	path := joinJSONPath(prefix, field)
	s.pathCache[cacheKey] = path
	return path
}

func (s *jsonStructScanner) arrayPath(prefix string) string {
	if prefix == "" {
		return ""
	}
	cacheKey := jsonPathCacheKey{prefix: prefix, array: true}
	if path, ok := s.pathCache[cacheKey]; ok {
		return path
	}
	if s.pathCache == nil {
		s.pathCache = make(map[jsonPathCacheKey]string)
	}
	path := prefix + "[]"
	s.pathCache[cacheKey] = path
	return path
}

func (s *jsonStructScanner) noteUnknownPath(prefix string, key []byte) {
	// Once the diagnostic is saturated, no later key can affect its result.
	// Return before materializing key: a request may contain millions more
	// unknown fields, and copying one bounded string per occurrence would put
	// allocation and GC back under the payload's control after the cap had
	// already done its job.
	if s.truncated {
		return
	}

	// Compare against the bounded retained set before copying key. Linear search
	// is bounded by maxUnknownFieldPaths and lets a repeated []byte key be
	// compared without materializing a temporary map key.
	for _, pathKey := range s.seen {
		if pathKey.prefix == prefix && unknownFieldKeyMatches(pathKey.key, key) {
			return
		}
	}
	if len(s.paths) >= maxUnknownFieldPaths {
		s.truncated = true
		return
	}

	// key still points into the document or reusable scratch, so this is where
	// it stops doing that, and the only place the scanner copies one at all.
	pathKey := unknownPathKey{prefix: prefix, key: unknownFieldKeyText(key)}
	s.seen = append(s.seen, pathKey)
	s.paths = append(s.paths, joinJSONPath(prefix, pathKey.key))
}

func (s *jsonStructScanner) skipSpace() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\n', '\r':
			s.pos++
		default:
			return
		}
	}
}

func (s *jsonStructScanner) peek() (byte, bool) {
	s.skipSpace()
	if s.pos >= len(s.data) {
		return 0, false
	}
	return s.data[s.pos], true
}

// scanValue walks one value, descending only where targetType can describe
// what is inside. Anything else is skipped without being materialized.
func (s *jsonStructScanner) scanValue(targetType reflect.Type, prefix string) error {
	next, ok := s.peek()
	if !ok {
		return fmt.Errorf("unexpected end of JSON input")
	}

	resolved := dereferenceType(targetType)
	inspect := resolved != nil && !shouldSkipUnknownFieldDetection(resolved)

	switch next {
	case '{':
		if inspect && resolved.Kind() == reflect.Struct {
			return s.scanObject(resolved, prefix)
		}
		return s.skipValue()
	case '[':
		if inspect && (resolved.Kind() == reflect.Slice || resolved.Kind() == reflect.Array) {
			return s.scanArray(resolved.Elem(), s.arrayPath(prefix))
		}
		return s.skipValue()
	default:
		return s.skipValue()
	}
}

func (s *jsonStructScanner) scanObject(targetType reflect.Type, prefix string) error {
	if err := s.expect('{'); err != nil {
		return err
	}
	if err := s.enterContainer('{'); err != nil {
		return err
	}
	defer s.leaveContainer()
	fieldTypes := getStructJSONFieldTypes(targetType)

	if next, ok := s.peek(); ok && next == '}' {
		s.pos++
		return nil
	}
	for {
		key, err := s.scanKey()
		if err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}

		// Indexing a map with string(bytes) is the one conversion the compiler
		// does not have to allocate for, so a key of any length is recognized or
		// refused without being copied.
		if child, known := fieldTypes[string(key)]; known {
			if err := s.scanValue(child.targetType, s.fieldPath(prefix, child.name)); err != nil {
				return err
			}
		} else {
			s.noteUnknownPath(prefix, key)
			if err := s.skipValue(); err != nil {
				return err
			}
		}

		next, ok := s.peek()
		if !ok {
			return fmt.Errorf("unexpected end of JSON object")
		}
		s.pos++
		switch next {
		case ',':
			continue
		case '}':
			return nil
		default:
			return fmt.Errorf("invalid character %q in JSON object", next)
		}
	}
}

func (s *jsonStructScanner) scanArray(elemType reflect.Type, prefix string) error {
	if err := s.expect('['); err != nil {
		return err
	}
	if err := s.enterContainer('['); err != nil {
		return err
	}
	defer s.leaveContainer()
	if next, ok := s.peek(); ok && next == ']' {
		s.pos++
		return nil
	}
	for {
		if err := s.scanValue(elemType, prefix); err != nil {
			return err
		}
		next, ok := s.peek()
		if !ok {
			return fmt.Errorf("unexpected end of JSON array")
		}
		s.pos++
		switch next {
		case ',':
			continue
		case ']':
			return nil
		default:
			return fmt.Errorf("invalid character %q in JSON array", next)
		}
	}
}

func (s *jsonStructScanner) expect(char byte) error {
	next, ok := s.peek()
	if !ok {
		return fmt.Errorf("unexpected end of JSON input, want %q", char)
	}
	if next != char {
		return fmt.Errorf("invalid character %q, want %q", next, char)
	}
	s.pos++
	return nil
}

func (s *jsonStructScanner) enterContainer(open byte) error {
	if s.nestingDepth >= maxJSONNestingDepth {
		return fmt.Errorf("invalid character %q exceeded max depth", open)
	}
	s.nestingDepth++
	return nil
}

func (s *jsonStructScanner) leaveContainer() {
	s.nestingDepth--
}

// scanKey reads an object key in a form suitable for struct matching and
// bounded reporting. Ordinary keys point into the document. Escaped keys short
// enough to match a declared field, and invalid UTF-8 that can survive the
// reporting cap, use scanner-owned scratch overwritten by the next key. In
// either case noteUnknownPath copies only a distinct bounded key it keeps.
// Searching for an escape as bytes matters for the same reason — converting to
// string to ask strings.ContainsRune copied every key in the body to answer a
// question one byte wide.
//
// An escaped key within the gate is decoded so it can still match a struct tag.
// The gate is the longest a key worth keeping can be written, not the longest
// one can be kept: escapes only ever shrink, so a key inside
// maxUnknownFieldKeyBytes*maxJSONEscapeBytes might still decode to a name this
// proxy declares, and one past it cannot. The decode is bounded by the same
// arithmetic, so a document the client sized buys a few hundred bytes.
func (s *jsonStructScanner) scanKey() ([]byte, error) {
	s.skipSpace()
	start := s.pos
	if err := s.skipString(); err != nil {
		return nil, err
	}
	quoted := s.data[start:s.pos]
	if len(quoted) < 2 {
		return nil, fmt.Errorf("invalid JSON object key")
	}
	unquoted := quoted[1 : len(quoted)-1]
	// Once collection is truncated, no decoded key can affect the result. Keep
	// parsing the document structurally, but skip even the bounded decode work.
	if s.truncated {
		return unquoted, nil
	}
	if len(unquoted) <= maxUnknownFieldKeyBytes*maxJSONEscapeBytes && bytes.IndexByte(unquoted, '\\') >= 0 {
		return s.decodeJSONKey(unquoted)
	}

	// encoding/json replaces malformed UTF-8 in strings with RuneError. Check
	// only the bytes that can survive the diagnostic cap; invalid bytes later in
	// a long key cannot affect its retained prefix. The extra UTFMax bytes avoid
	// treating a valid rune crossing the cap as malformed.
	prefix := unquoted
	if len(prefix) > maxUnknownFieldKeyBytes+utf8.UTFMax {
		prefix = prefix[:maxUnknownFieldKeyBytes+utf8.UTFMax]
	}
	if utf8.Valid(prefix) {
		return unquoted, nil
	}
	decoded := s.decodedKeyScratch[:0]
	for pos := 0; pos < len(unquoted) && len(decoded) <= maxUnknownFieldKeyBytes; {
		value, size := utf8.DecodeRune(unquoted[pos:])
		decoded = utf8.AppendRune(decoded, value)
		pos += size
	}
	return decoded, nil
}

// decodeJSONKey applies encoding/json's string-unquoting rules into reusable
// scanner storage. The input length gate in scanKey makes the destination
// statically bounded, including replacement of malformed UTF-8 with RuneError.
func (s *jsonStructScanner) decodeJSONKey(encoded []byte) ([]byte, error) {
	decoded := s.decodedKeyScratch[:0]
	for pos := 0; pos < len(encoded); {
		char := encoded[pos]
		switch {
		case char == '\\':
			pos++
			if pos >= len(encoded) {
				return nil, fmt.Errorf("invalid JSON object key")
			}
			switch encoded[pos] {
			case '"', '\\', '/':
				decoded = append(decoded, encoded[pos])
				pos++
			case 'b':
				decoded = append(decoded, '\b')
				pos++
			case 'f':
				decoded = append(decoded, '\f')
				pos++
			case 'n':
				decoded = append(decoded, '\n')
				pos++
			case 'r':
				decoded = append(decoded, '\r')
				pos++
			case 't':
				decoded = append(decoded, '\t')
				pos++
			case 'u':
				value, ok := decodeJSONHexRune(encoded[pos+1:])
				if !ok {
					return nil, fmt.Errorf("invalid JSON object key")
				}
				pos += 5
				if utf16.IsSurrogate(value) {
					value = decodeJSONSurrogate(encoded, &pos, value)
				}
				decoded = utf8.AppendRune(decoded, value)
			default:
				return nil, fmt.Errorf("invalid JSON object key")
			}
		case char == '"' || char < ' ':
			return nil, fmt.Errorf("invalid JSON object key")
		case char < utf8.RuneSelf:
			decoded = append(decoded, char)
			pos++
		default:
			value, size := utf8.DecodeRune(encoded[pos:])
			decoded = utf8.AppendRune(decoded, value)
			pos += size
		}
	}
	return decoded, nil
}

func decodeJSONSurrogate(encoded []byte, pos *int, first rune) rune {
	if *pos+6 > len(encoded) || encoded[*pos] != '\\' || encoded[*pos+1] != 'u' {
		return unicode.ReplacementChar
	}
	second, ok := decodeJSONHexRune(encoded[*pos+2:])
	if !ok {
		return unicode.ReplacementChar
	}
	decoded := utf16.DecodeRune(first, second)
	if decoded == unicode.ReplacementChar {
		return decoded
	}
	*pos += 6
	return decoded
}

func decodeJSONHexRune(encoded []byte) (rune, bool) {
	if len(encoded) < 4 {
		return 0, false
	}
	var value rune
	for _, char := range encoded[:4] {
		value <<= 4
		switch {
		case '0' <= char && char <= '9':
			value += rune(char - '0')
		case 'a' <= char && char <= 'f':
			value += rune(char-'a') + 10
		case 'A' <= char && char <= 'F':
			value += rune(char-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

// skipString advances past a quoted string while validating the JSON escape
// grammar. Invalid UTF-8 is deliberately accepted, matching encoding/json,
// which replaces it with RuneError when materializing a string.
func (s *jsonStructScanner) skipString() error {
	s.skipSpace()
	if s.pos >= len(s.data) || s.data[s.pos] != '"' {
		return fmt.Errorf("expected a JSON string")
	}
	s.pos++
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case '\\':
			s.pos++
			if s.pos >= len(s.data) {
				return fmt.Errorf("unterminated JSON string escape")
			}
			switch s.data[s.pos] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				s.pos++
			case 'u':
				if _, ok := decodeJSONHexRune(s.data[s.pos+1:]); !ok {
					return fmt.Errorf("invalid JSON unicode escape")
				}
				s.pos += 5
			default:
				return fmt.Errorf("invalid JSON string escape %q", s.data[s.pos])
			}
		case '"':
			s.pos++
			return nil
		default:
			if s.data[s.pos] < ' ' {
				return fmt.Errorf("invalid control character in JSON string")
			}
			s.pos++
		}
	}
	return fmt.Errorf("unterminated JSON string")
}

// skipValue advances past one value of any type without allocating.
func (s *jsonStructScanner) skipValue() error {
	next, ok := s.peek()
	if !ok {
		return fmt.Errorf("unexpected end of JSON input")
	}
	switch next {
	case '"':
		return s.skipString()
	case '{':
		return s.skipObjectValue()
	case '[':
		return s.skipArrayValue()
	default:
		return s.skipLiteral()
	}
}

func (s *jsonStructScanner) skipObjectValue() error {
	if err := s.expect('{'); err != nil {
		return err
	}
	if err := s.enterContainer('{'); err != nil {
		return err
	}
	defer s.leaveContainer()
	if next, ok := s.peek(); ok && next == '}' {
		s.pos++
		return nil
	}
	for {
		if err := s.skipString(); err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := s.skipValue(); err != nil {
			return err
		}

		next, ok := s.peek()
		if !ok {
			return fmt.Errorf("unexpected end of JSON object")
		}
		s.pos++
		switch next {
		case ',':
			continue
		case '}':
			return nil
		default:
			return fmt.Errorf("invalid character %q in JSON object", next)
		}
	}
}

func (s *jsonStructScanner) skipArrayValue() error {
	if err := s.expect('['); err != nil {
		return err
	}
	if err := s.enterContainer('['); err != nil {
		return err
	}
	defer s.leaveContainer()
	if next, ok := s.peek(); ok && next == ']' {
		s.pos++
		return nil
	}
	for {
		if err := s.skipValue(); err != nil {
			return err
		}

		next, ok := s.peek()
		if !ok {
			return fmt.Errorf("unexpected end of JSON array")
		}
		s.pos++
		switch next {
		case ',':
			continue
		case ']':
			return nil
		default:
			return fmt.Errorf("invalid character %q in JSON array", next)
		}
	}
}

// skipLiteral advances past and validates a JSON number, true, false, or null.
func (s *jsonStructScanner) skipLiteral() error {
	if s.pos >= len(s.data) {
		return fmt.Errorf("unexpected end of JSON input")
	}
	switch s.data[s.pos] {
	case 't':
		return s.skipKeyword("true")
	case 'f':
		return s.skipKeyword("false")
	case 'n':
		return s.skipKeyword("null")
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return s.skipNumber()
	default:
		return fmt.Errorf("invalid character %q at start of JSON value", s.data[s.pos])
	}
}

func (s *jsonStructScanner) skipKeyword(keyword string) error {
	if len(s.data)-s.pos < len(keyword) || !stringEqualsBytes(keyword, s.data[s.pos:s.pos+len(keyword)]) {
		return fmt.Errorf("invalid JSON literal")
	}
	s.pos += len(keyword)
	if s.pos < len(s.data) && !isJSONValueDelimiter(s.data[s.pos]) {
		return fmt.Errorf("invalid character %q after JSON literal", s.data[s.pos])
	}
	return nil
}

func (s *jsonStructScanner) skipNumber() error {
	start := s.pos
	if s.data[s.pos] == '-' {
		s.pos++
		if s.pos >= len(s.data) {
			return fmt.Errorf("invalid JSON number at byte %d", start)
		}
	}

	switch {
	case s.data[s.pos] == '0':
		s.pos++
		if s.pos < len(s.data) && isJSONDigit(s.data[s.pos]) {
			return fmt.Errorf("invalid leading zero in JSON number")
		}
	case '1' <= s.data[s.pos] && s.data[s.pos] <= '9':
		for s.pos < len(s.data) && isJSONDigit(s.data[s.pos]) {
			s.pos++
		}
	default:
		return fmt.Errorf("invalid JSON number at byte %d", start)
	}

	if s.pos < len(s.data) && s.data[s.pos] == '.' {
		s.pos++
		fractionStart := s.pos
		for s.pos < len(s.data) && isJSONDigit(s.data[s.pos]) {
			s.pos++
		}
		if s.pos == fractionStart {
			return fmt.Errorf("JSON number has no digits after decimal point")
		}
	}

	if s.pos < len(s.data) && (s.data[s.pos] == 'e' || s.data[s.pos] == 'E') {
		s.pos++
		if s.pos < len(s.data) && (s.data[s.pos] == '+' || s.data[s.pos] == '-') {
			s.pos++
		}
		exponentStart := s.pos
		for s.pos < len(s.data) && isJSONDigit(s.data[s.pos]) {
			s.pos++
		}
		if s.pos == exponentStart {
			return fmt.Errorf("JSON number has no exponent digits")
		}
	}

	if s.pos < len(s.data) && !isJSONValueDelimiter(s.data[s.pos]) {
		return fmt.Errorf("invalid character %q after JSON number", s.data[s.pos])
	}
	return nil
}

func isJSONDigit(char byte) bool {
	return '0' <= char && char <= '9'
}

func isJSONValueDelimiter(char byte) bool {
	switch char {
	case ',', '}', ']', ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

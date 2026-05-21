package proxy

// Converter handles API format conversions. Provider formats should convert
// through TypedRequest whenever a typed representation is available.
type Converter struct{}

// NewConverter creates a new converter
func NewConverter() *Converter {
	return &Converter{}
}

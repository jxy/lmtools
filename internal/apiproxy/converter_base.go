package apiproxy

// Converter handles conversions between different API formats
type Converter struct {
	mapper *ModelMapper
}

// NewConverter creates a new converter
func NewConverter(mapper *ModelMapper) *Converter {
	return &Converter{mapper: mapper}
}

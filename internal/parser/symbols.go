package parser

// ExtractedSymbol holds a symbol found by parsing.
type ExtractedSymbol struct {
	Name       string
	Kind       string // function, method, type, interface, struct, class, variable
	Line       int    // 1-based
	EndLine    int    // 1-based
	Signature  string
	DocComment string
	ParentName string   // for methods: the receiver/class name
	References []string // names of symbols called/referenced in the body
}

package styles

// Semantic glyph functions keep terminal-width assumptions in one place. The
// Unicode choices are text symbols with a measured width of one cell; notably,
// DocumentGlyph avoids emoji presentation and its terminal-dependent width.
func OpenCodeGlyph() string { return OneCellGlyph("⌬", "#") }
func CheckGlyph() string    { return OneCellGlyph("✓", "+") }
func ErrorGlyph() string    { return OneCellGlyph("×", "x") }
func WarningGlyph() string  { return OneCellGlyph("▲", "!") }
func InfoGlyph() string     { return OneCellGlyph("i", "i") }
func HintGlyph() string     { return OneCellGlyph("?", "?") }
func SpinnerGlyph() string  { return OneCellGlyph("·", ".") }
func LoadingGlyph() string  { return OneCellGlyph("↻", "~") }
func DocumentGlyph() string { return OneCellGlyph("▣", "@") }

// These legacy names remain strings so existing renderers do not need to
// change in lockstep. They capture the process terminal mode during package
// initialization, which matches CLI operation; new rendering code should call
// the semantic functions when it needs runtime capability detection.
var (
	OpenCodeIcon string = OpenCodeGlyph()

	CheckIcon    string = CheckGlyph()
	ErrorIcon    string = ErrorGlyph()
	WarningIcon  string = WarningGlyph()
	InfoIcon     string = InfoGlyph()
	HintIcon     string = HintGlyph()
	SpinnerIcon  string = SpinnerGlyph()
	LoadingIcon  string = LoadingGlyph()
	DocumentIcon string = DocumentGlyph()
)

package dashboard

// stylesheet is served raw at /static/style.css. CASCADE ORDER IS THE
// CONTRACT: later rules deliberately override earlier ones, so the
// concatenation below is the single source of truth for section order.
// Every styles* section const (defined across the styles_*.go files)
// appears here exactly once, in the original order of the stylesheet.
// Add new sections by appending; never reorder without a design review.
var stylesheet = stylesBase +
	stylesLookup +
	stylesChrome +
	stylesChat +
	stylesLogin +
	stylesProfile +
	stylesAttachment +
	stylesReading +
	stylesComments +
	stylesOps +
	stylesEntryLists +
	stylesHome +
	stylesTalk

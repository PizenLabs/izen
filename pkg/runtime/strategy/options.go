package strategy

// configurable is implemented by the built-in strategies so shared options
// can configure either one.
type configurable interface {
	setSystem(string)
	setMaxTokens(int)
}

// Option configures a built-in strategy.
type Option func(configurable)

// WithSystem overrides the strategy system prompt.
func WithSystem(system string) Option {
	return func(c configurable) {
		if system != "" {
			c.setSystem(system)
		}
	}
}

// WithMaxTokens caps the tokens a single generation pass may emit.
func WithMaxTokens(n int) Option {
	return func(c configurable) {
		if n > 0 {
			c.setMaxTokens(n)
		}
	}
}

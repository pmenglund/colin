package service

type options struct {
	serverPortOverride *int
}

// Option configures optional service behavior layered on top of WORKFLOW.md.
type Option func(*options)

// WithServerPortOverride forces the dashboard to bind to the supplied port.
func WithServerPortOverride(port int) Option {
	return func(opts *options) {
		opts.serverPortOverride = &port
	}
}

func buildOptions(optionFns ...Option) options {
	var opts options
	for _, fn := range optionFns {
		if fn != nil {
			fn(&opts)
		}
	}
	return opts
}

package proxyguard

// Logger is the interface to log messages
type Logger interface {
	// Log logs a single message
	Log(_ string)
	// Logf logs a message with arguments
	Logf(_ string, _ ...interface{})
}

type nullLogger struct{}

// Log logs a single message and does nothing here
func (l nullLogger) Log(_ string) {}

// Logf logs a message with arguments and does nothing here
func (l nullLogger) Logf(_ string, _ ...interface{}) {}

var log Logger = nullLogger{}

// UpdateLogger updates the logger
func UpdateLogger(l Logger) {
	log = l
}

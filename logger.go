package gotsr

type Logger interface {
	Print(v ...interface{})
	Printf(format string, v ...interface{})
	Println(v ...interface{})
}

var lg Logger = nilLogger{}

// SetLogger sets the logger for the package.  If not set, the package will be
// silent.  The default logger is a nilLogger.  If TSR is initialised with
// with WithDebug(true) option, the default logger will be set to a standard
// Go logger.
func SetLogger(l Logger) {
	lg = l
}

type nilLogger struct{}

func (nilLogger) Print(v ...interface{})                 {}
func (nilLogger) Printf(format string, v ...interface{}) {}
func (nilLogger) Println(v ...interface{})               {}

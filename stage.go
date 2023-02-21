package gotsr

// stage is the initialisation stage of the program.
//
//go:generate stringer -type stage -linecomment
type stage int8

const (
	sUnknown    stage = -1 + iota // UNKNOWN
	sInitialise                   // INIT
	sDetach                       // DETACH
	sRunning                      // RUN
)

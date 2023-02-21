package gotsr

// envVar is a unique identifier for the environment variables used by TSR.
type envVar string

// newEnvVar returns a new unique identifier for the environment variables.
// It is calculated as the first 7 characters of the SHA1 hash of the given
// string.
func newEnvVar(s string) envVar {
	return envVar(hash(s)[0:7])
}

// stage returns the name of the environment variable that holds the stage.
func (id envVar) stage() string {
	return "TSR_" + string(id) + "__STG"
}

// pid returns the name of the environment variable that holds the PID.
func (id envVar) pid() string {
	return "TSR_" + string(id) + "__PID"
}

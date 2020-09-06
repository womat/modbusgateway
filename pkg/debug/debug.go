package debug

var debug bool

func On() bool {
	debug = true
	return State()
}
func Off() bool {
	debug = false
	return State()
}
func State() bool {
	return debug
}

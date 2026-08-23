package core

// BrowserSubmissions carries prompts submitted from the web dashboard into the
// agent loop. It lives in core so the web package (producer) and the engine /
// tui (consumers) can share it without importing each other — the same neutral
// role the event bus plays.
var BrowserSubmissions = make(chan string, 16)

// DrainBrowserSubmission returns a queued browser prompt if one is waiting,
// or "" immediately if none. Non-blocking.
func DrainBrowserSubmission() string {
	select {
	case s := <-BrowserSubmissions:
		return s
	default:
		return ""
	}
}

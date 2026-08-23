package agent

// Internal aliases: the engine's public API is exported (RunTurn, etc.) for
// the tui/web packages; these lowercase aliases keep existing in-package call
// sites working unchanged during the split.

var runTurn = RunTurn
var runVerifyLoop = RunVerifyLoop
var runInfoCommand = RunInfoCommand
var compact = Compact
var execShell = ExecShell
var setApprovalMode = SetApprovalMode
var tail = Tail

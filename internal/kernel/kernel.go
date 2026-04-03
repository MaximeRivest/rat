// Package kernel defines the interface that every language kernel must implement.
//
// A kernel is a running REPL subprocess (Python, R, Julia, bash, etc.)
// that executes code, inspects variables, and provides completions.
// The MCP server talks to the kernel through this interface.
// Each language implements it differently, but the contract is the same.
package kernel

// Kernel is the interface every language runtime must implement.
// Three methods — matching the three MCP tools: run, look, ctl.
type Kernel interface {
	// Run executes code and returns the result.
	// stdout/stderr are captured. State persists between calls.
	Run(code string) RunResult

	// SendInput writes text to the running process's stdin.
	// Used for interactive prompts (passwords, confirmations, read).
	// The text is written directly to the PTY — no execution wrapper.
	SendInput(text string) error

	// IsWaitingForInput returns true if the running process is blocked
	// waiting for stdin. Lock-free — safe to call during execution.
	IsWaitingForInput() bool

	// Look inspects the runtime state.
	// No args: variable overview. At="x": inspect x. Code+Cursor: completions.
	Look(req LookRequest) LookResult

	// Ctl controls the runtime.
	// ops: "reset" (clear namespace), "cancel" (interrupt), "restart" (fresh process).
	Ctl(op string) CtlResult

	// Shutdown tears down the kernel subprocess.
	Shutdown() error
}

// RunResult is what comes back from executing code.
type RunResult struct {
	Success   bool   // did it exit cleanly?
	Output    string // combined stdout (main output for the user)
	Error     string // error message if !Success
	ExecCount int    // how many executions so far
	Duration  int    // milliseconds
	Vars      int    // number of user-visible variables (0 = unknown)
}

// LookRequest is the input to Look.
// Exactly one of these patterns:
//   - nothing set → overview (variable list)
//   - At set → inspect that symbol
//   - Code+Cursor set → completions at cursor position
type LookRequest struct {
	At     string // symbol to inspect ("x", "df.columns", "math.sqrt")
	Code   string // code buffer for completions
	Cursor int    // cursor position in Code
}

// LookResult is what comes back from Look.
type LookResult struct {
	Text string // formatted text (same format as rat-py's output)
}

// CtlResult is what comes back from a control operation.
type CtlResult struct {
	Text string // status message
}

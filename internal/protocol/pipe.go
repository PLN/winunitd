package protocol

// DefaultPipeName is the system manager control pipe (DESIGN.md §30).
const DefaultPipeName = `\\.\pipe\winunitd\control`

// ControlPipeSDDL allows LocalSystem and Administrators only.
// D:P — protected DACL (no inherited ACEs). GA — generic all.
// SY — Local System (S-1-5-18). BA — Builtin Administrators (S-1-5-32-544).
const ControlPipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

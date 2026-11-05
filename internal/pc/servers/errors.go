package servers

import "errors"

// The error kinds of this package. Test them with errors.Is.
var (
	// ErrInvalidInput: the input is malformed, incomplete or rejected by the dry run.
	ErrInvalidInput = errors.New("servers: invalid input")
	// ErrUnsupportedProtocol: a protocol, transport or option this version cannot use.
	ErrUnsupportedProtocol = errors.New("servers: unsupported protocol")
	// ErrDuplicate: a server with the same protocol, host, port and credential exists.
	ErrDuplicate = errors.New("servers: duplicate server")
	// ErrLimit: the store already holds MaxServers servers.
	ErrLimit = errors.New("servers: too many servers")
	// ErrNotFound: no server with that id.
	ErrNotFound = errors.New("servers: server not found")
	// ErrTooLarge: the input is longer than MaxInputBytes.
	ErrTooLarge = errors.New("servers: input too large")
)

// Error is a failure whose message is safe to show to the user: a fixed
// sentence that names the kind of problem and never a value from the input.
type Error struct {
	kind error
	msg  string
}

// Error returns the user-safe sentence.
func (e *Error) Error() string { return e.msg }

// Is matches the error kind (ErrInvalidInput, ErrUnsupportedProtocol, ...).
func (e *Error) Is(target error) bool { return target == e.kind }

// UserMessage returns the same fixed sentence; the gateway uses it through an
// interface so it does not import this package.
func (e *Error) UserMessage() string { return e.msg }

func newError(kind error, msg string) *Error { return &Error{kind: kind, msg: msg} }

func invalid(msg string) *Error     { return newError(ErrInvalidInput, msg) }
func unsupported(msg string) *Error { return newError(ErrUnsupportedProtocol, msg) }

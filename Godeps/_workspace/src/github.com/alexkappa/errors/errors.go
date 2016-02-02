package errors

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strconv"
)

type Error interface {
	// Message returns the error message of the error.
	Message() string
	// Inner returns the inner error that this error wraps.
	Inner() error
	// Stack returns the stack trace that led to the error.
	Stack() Frames
	// Error satisfies the standard library error interface.
	Error() string
}

var (
	// This setting enables a stack trace when an Error is being printed.
	//
	// 	err := errors.New("example")
	// 	err.Error() // "example" [<function>(<file>:<line>), ...]
	PrintTrace = true
	// This setting enables a stack trace to be printed when an Error is being
	// marshaled.
	//
	// 	err := errors.New("example")
	// 	b, _ := json.Marshal(err) // {"message":"example","stack":[{"file":"<file>","line":<line>,"func": "<function>"},...]}
	MarshalTrace = false
)

// Type errtype is the default implementation of the Error interface. It is not
// exported so users can only use it via the New or Wrap functions.
type errtype struct {
	M string `json:"message"`
	I error  `json:"inner,omitempty"`
	S Frames `json:"stack,omitempty"`
}

// Message returns the error message of the error.
func (t *errtype) Message() string {
	return t.M
}

// Inner returns the inner error that this error wraps.
func (t *errtype) Stack() Frames {
	return t.S
}

// Stack returns the stack trace that led to the error.
func (t *errtype) Inner() error {
	return t.I
}

// Error satisfies the standard library error interface.
func (t *errtype) Error() string {
	var buf bytes.Buffer
	buf.WriteString(t.M)
	if t.I != nil {
		buf.WriteByte('.')
		buf.WriteByte(' ')
		buf.WriteString(t.I.Error())
	}
	if t.S != nil && PrintTrace {
		buf.WriteByte(' ')
		buf.WriteByte('[')
		buf.WriteString(t.S.String())
		buf.WriteByte(']')
	}
	return buf.String()
}

// New creates a new Error with the supplied message.
func New(message string) Error {
	return new(message, 3)
}

func new(message string, skip int) Error {
	return &errtype{
		M: message,
		S: Stack(skip),
	}
}

// Wrap creates a new Error that wraps err.
func Wrap(err error, message string) Error {
	return wrap(err, message, 3)
}

func wrap(err error, message string, skip int) Error {
	if errT, ok := err.(*errtype); ok {
		errT.S = nil // drop the stack trace of the inner error.
	} else {
		err = &errtype{M: err.Error()}
	}
	return &errtype{
		M: message,
		I: err,
		S: Stack(skip),
	}
}

// Frame contains information for a single stack frame.
type Frame struct {
	// File is the path to the file of the caller.
	File string `json:"file"`
	// Line is the line in the file where the function call was made.
	Line int `json:"line"`
	// Func is the name of the caller.
	Func string `json:"func"`
}

type Frames []Frame

// String is used to satisfy the fmt.Stringer interface. It formats the stack
// trace as a comma separated list of "file:line function".
func (f Frames) String() string {
	var buf bytes.Buffer
	for i, frame := range f {
		buf.WriteString(frame.Func)
		buf.WriteByte('(')
		buf.WriteString(frame.File)
		buf.WriteByte(':')
		buf.WriteString(strconv.Itoa(frame.Line))
		buf.WriteByte(')')
		if i < len(f)-1 {
			buf.WriteByte(',')
		}
	}
	return buf.String()
}

func (f Frames) MarshalJSON() ([]byte, error) {
	if !MarshalTrace {
		return []byte("[]"), nil
	}
	b := bytes.NewBuffer(nil)
	b.WriteByte('[')
	e := json.NewEncoder(b)
	for i, frame := range f {
		err := e.Encode(frame)
		if err != nil {
			return b.Bytes(), err
		}
		if i+1 < len(f) {
			b.WriteByte(',')
		}
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}

// Stack returns the stack trace of the function call, while skipping the first
// skip frames.
func Stack(skip int) Frames {
	callers := make([]uintptr, 10)
	n := runtime.Callers(skip, callers)
	callers = callers[:n-2] // skip runtime.main and runtime.goexit function calls
	frames := make(Frames, len(callers))
	for i, caller := range callers {
		fn := runtime.FuncForPC(caller)
		file, line := fn.FileLine(caller)
		frames[i] = Frame{
			File: filepath.Base(file),
			Line: line,
			Func: fn.Name(),
		}
	}
	return frames
}

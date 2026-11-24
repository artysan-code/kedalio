package tasks

import "fmt"

// RefusedError is a call the caller has to change (a title too long, a time without a day, a step still open), as
// against a failure of the database, which is not the caller's to fix: the MCP handler shows its text to the client.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return e.Msg }

func refusef(format string, args ...any) error { return &RefusedError{fmt.Sprintf(format, args...)} }

func refuse(msg string) error { return &RefusedError{msg} }

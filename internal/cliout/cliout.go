// Package cliout is the single JSON writer for every pika command.
// Before it, check emitted compact JSON, adopt/apply/init emitted
// indented JSON, and init built its payload inside internal/initcmd.
// Agents parse this surface; it cannot be per-command folklore.
package cliout

import (
	"encoding/json"
	"fmt"
	"io"
)

// Schema is the envelope version. Bump only on a breaking shape change.
const Schema = 1

// ErrorBody reports a usage or configuration failure.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Envelope is the shape of every --json payload. Result carries the
// command's own report type unchanged, so existing report structs keep
// their shape and only gain nesting.
type Envelope struct {
	Schema  int             `json:"schema"`
	Command string          `json:"command"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ErrorBody      `json:"error,omitempty"`
}

// Write emits a successful or failed command result. ok=false implies the
// caller returns exit 1.
func Write(w io.Writer, command string, ok bool, result any) error {
	env := Envelope{Schema: Schema, Command: command, OK: ok}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("cliout: marshal result: %w", err)
		}
		env.Result = raw
	}
	return encode(w, env)
}

// WriteError emits a usage or configuration failure; the caller returns
// exit 2.
func WriteError(w io.Writer, command, code, message string) error {
	return encode(w, Envelope{
		Schema:  Schema,
		Command: command,
		OK:      false,
		Error:   &ErrorBody{Code: code, Message: message},
	})
}

func encode(w io.Writer, env Envelope) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		return fmt.Errorf("cliout: %w", err)
	}
	return nil
}

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// This file holds the one thing a smoke gate is for: saying loudly what
// was expected and what happened.
//
// A gate that reports "step improve failed" and nothing else costs an
// operator the whole investigation it was supposed to have done for
// them. Every assertion here carries three things — the name of the
// claim, the value the product was supposed to produce, and the value it
// produced — and a failing step reports ALL of its disagreements rather
// than the first, because "the commit is empty" and "the branch is
// wrong" are one diagnosis together and two dead ends apart.

// outputExcerpt bounds how much of a command's output a failure message
// reproduces. Enough to hold a whole check report; short of pasting a
// megabyte of test output into a CI log.
const outputExcerpt = 6000

// check accumulates the disagreements one step found.
type check struct {
	problems []string
}

// failf records one disagreement.
func (c *check) failf(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

// truef records a disagreement when cond is false. It is the escape
// hatch for claims that are not an equality or a substring; the message
// is the assertion's whole content, so it has to state both sides
// itself.
func (c *check) truef(cond bool, format string, args ...any) {
	if !cond {
		c.failf(format, args...)
	}
}

// contains asserts that got names every string in want. The missing ones
// are reported together with one copy of the text, because a message
// that repeats a 6KB check report once per missing needle is a message
// nobody reads.
func (c *check) contains(what, got string, want ...string) {
	var missing []string
	for _, w := range want {
		if !strings.Contains(got, w) {
			missing = append(missing, strconv.Quote(w))
		}
	}
	if len(missing) == 0 {
		return
	}
	c.failf("%s does not name %s\n%s", what, strings.Join(missing, ", "), quoteBlock(what, got))
}

// absent asserts that got carries none of the unwanted strings. It is
// how a repaired message states what an operator must never be handed
// again: the branch defect's remedy is not that the new message is
// helpful, it is that Git's bare `exit status 128` is gone.
func (c *check) absent(what, got string, unwanted ...string) {
	var present []string
	for _, w := range unwanted {
		if strings.Contains(got, w) {
			present = append(present, strconv.Quote(w))
		}
	}
	if len(present) == 0 {
		return
	}
	c.failf("%s still carries %s\n%s", what, strings.Join(present, ", "), quoteBlock(what, got))
}

// err renders every disagreement this step found, or nil when it found
// none.
func (c *check) err() error {
	if len(c.problems) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d assertion(s) failed", len(c.problems))
	for i, p := range c.problems {
		fmt.Fprintf(&b, "\n\n  (%d) %s", i+1, indentRest(p, "      "))
	}
	return errors.New(b.String())
}

// wantEqual asserts one observed value against the one the product is
// supposed to produce. It is a function rather than a method because Go
// methods cannot be generic, and comparing through `any` would turn a
// slice argument into a panic inside the assertion library.
func wantEqual[T comparable](c *check, what string, got, want T) {
	if got != want {
		c.failf("%s: expected %v, got %v", what, want, got)
	}
}

// quoteBlock renders a command's output as a delimited block. The
// delimiters name what is inside them: a wall of text in a CI log with
// no header above it is indistinguishable from the harness's own noise.
func quoteBlock(what, got string) string {
	return fmt.Sprintf("--- %s ---\n%s\n--- end %s ---", what, excerpt(got, outputExcerpt), what)
}

// excerpt bounds s, and says how much it dropped rather than trailing
// off. A truncation that does not admit to being one reads as a product
// that stopped mid-sentence.
func excerpt(s string, max int) string {
	s = strings.TrimRight(s, "\n")
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n… [%d more bytes]", len(s)-max)
}

// indentRest indents every line of s after the first, so a multi-line
// problem stays visually inside the numbered item that introduced it.
func indentRest(s, pad string) string {
	return strings.ReplaceAll(s, "\n", "\n"+pad)
}

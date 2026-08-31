package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Choaterboater/pika/internal/repopath"
)

// This file holds the file surgery: where a kernel-owned region begins
// and ends inside a document, how it is read back, and how it is
// replaced without touching a byte the operator wrote.
//
// It is separate from the rendering next door because the two answer
// different questions and are shared differently. Rendering is about
// what a region says, and every target class renders the same way from
// different inputs. This is about where a region lives, and every target
// class — a repository projection, an agent file in the operator's home
// directory — goes through exactly these functions. Two implementations
// of "is this still what pika generated" would eventually disagree about
// a hand edit, and the one they disagreed about would be the file a
// harness actually reads.

// inspectRegionFile is the verdict on one file that carries a
// kernel-owned region, wherever that file lives: whether it is there,
// whether somebody edited kernel-owned bytes, and whether it still says
// what b renders.
//
// The order of the two failing verdicts is deliberate. A region is
// checked against its OWN recorded digest before it is compared to
// anything its sources render to, because those two failures have
// opposite remedies: regenerating a stale copy costs nothing, and
// regenerating a tampered one destroys whatever somebody typed there.
// Asking "was this region edited?" first answers that question from the
// file alone, so a hand edit stays visible even when a source moved at
// the same time.
func inspectRegionFile(target string, b body) (state, detail string) {
	doc, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		return StateAbsent, "does not exist; write it with " + b.from.command
	}
	if err != nil {
		return StateUnreadable, fmt.Sprintf("cannot be read: %v", err)
	}
	region, ok, err := extractRegion(doc)
	if err != nil {
		return StateUnreadable, err.Error()
	}
	if !ok {
		return StateAbsent, "carries no pika skills region; write it with " + b.from.command
	}
	if err := verifyRegion(region); err != nil {
		return StateTampered, tamperedDetail(err, b.from)
	}
	if bytes.Equal(region, b.region) {
		return StateCurrent, ""
	}
	return StateStale, staleDetail(region, b)
}

// insideRoot resolves a declared repository-relative path against the
// repository root and refuses one that lands outside it.
//
// contract.Load already rejects an absolute path, a `~` path and a `..`
// escape when it reads the file, and this is the same rule enforced
// again at the only place that opens a file for writing. That
// duplication is deliberate: this is the boundary that keeps a committed
// contract from reaching anything the repository does not own, and a
// boundary held only by the parser is one that a caller constructing a
// Contract in memory walks straight through.
func insideRoot(root *repopath.Root, rel string) (string, error) {
	target := filepath.Join(root.Dir(), filepath.FromSlash(rel))
	within, err := filepath.Rel(root.Dir(), target)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("declares the path %s, which resolves to %s — outside the repository root; a contract cannot direct pika to read or write outside the repository it governs", rel, target)
	}
	return target, nil
}

// writeRegionFile regenerates the kernel-owned region of one file in
// place and reports the result. A file whose markers do not pair up is
// refused rather than rewritten: the kernel owns the region, not the
// file, and it cannot tell where the region ends.
//
// preamble is text the file must open with when it has none of its own —
// the frontmatter that makes the omp skill file loadable at all. It is
// written only when the file is being created, because everything
// outside the markers belongs to whoever put it there, and restoring a
// preamble over an operator's own edited frontmatter would take back a
// half of the file the kernel does not own.
//
// An install is allowed to overwrite a hand-edited region — that region
// is kernel property and this is the command that asserts it — but it
// says when it did. An operator whose edit is gone learns it here
// rather than from a diff a week later.
func writeRegionFile(target string, preamble []byte, b body) (state, detail string, written bool, err error) {
	doc, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return StateUnreadable, fmt.Sprintf("cannot be read: %v", err), false, nil
	}
	if len(bytes.TrimSpace(doc)) == 0 {
		doc = preamble
	}
	next, err := splice(doc, b.region)
	if err != nil {
		return StateUnreadable, err.Error(), false, nil
	}
	if bytes.Equal(doc, next) {
		return StateCurrent, "", false, nil
	}
	if err := writeFile(target, next); err != nil {
		return StateUnreadable, "", false, err
	}
	if region, ok, err := extractRegion(doc); err == nil && ok {
		if bad := verifyRegion(region); bad != nil && !errors.Is(bad, errUnverifiable) {
			detail = "replaced a hand-edited kernel-owned region: it " + bad.Error() +
				"; whatever was typed there is gone — recover it from version control and put it in the source instead"
		}
	}
	return StateCurrent, detail, true, nil
}

// extractRegion returns the managed region of doc, markers included.
func extractRegion(doc []byte) ([]byte, bool, error) {
	start, end, err := regionBounds(doc)
	if err != nil || start < 0 {
		return nil, false, err
	}
	return doc[start:end], true, nil
}

// regionBounds locates the managed region: start is the index of the
// begin marker's line, end the index just past the newline that closes
// the end marker. start is -1 when there is no region. A second begin
// marker, or an end marker that never arrives, is an error — both are
// states where replacing "the" region would silently destroy text.
func regionBounds(doc []byte) (start, end int, err error) {
	begins := markerLines(doc, beginMarker)
	ends := markerLines(doc, endMarker)
	if len(begins) == 0 {
		if len(ends) > 0 {
			return -1, 0, fmt.Errorf("carries a %s with no matching %s; the kernel cannot tell where its region begins", endMarker, beginMarker)
		}
		return -1, 0, nil
	}
	if len(begins) > 1 {
		return -1, 0, fmt.Errorf("carries more than one %s; the kernel cannot tell which region is its own", beginMarker)
	}
	start = begins[0]
	for _, e := range ends {
		if e <= start {
			continue
		}
		end = e + len(endMarker)
		if end < len(doc) && doc[end] == '\n' {
			end++
		}
		return start, end, nil
	}
	return -1, 0, fmt.Errorf("carries a %s with no matching %s; the kernel cannot tell where its region ends", beginMarker, endMarker)
}

// markerLines returns the offset of every line in doc whose entire
// content is marker.
//
// A marker is a whole line, not a substring. Prose that names one —
// documentation explaining what the region is, which is the first thing
// anyone writes above it — mentions it inside an inline code span, and a
// substring search counts that as a second region and refuses to touch
// the file. Anchoring to the line makes the documentation and the
// mechanism able to coexist, which they have to: a marker nobody may
// write down is a marker nobody can explain.
func markerLines(doc []byte, marker string) []int {
	var out []int
	for offset := 0; offset < len(doc); {
		width := bytes.IndexByte(doc[offset:], '\n')
		line := doc[offset:]
		next := len(doc)
		if width >= 0 {
			line = doc[offset : offset+width]
			next = offset + width + 1
		}
		if string(bytes.TrimRight(line, "\r")) == marker {
			out = append(out, offset)
		}
		offset = next
	}
	return out
}

// splice returns doc with its managed region replaced by region,
// appending the region when doc has none. Everything outside the markers
// is the operator's text and comes back unchanged.
func splice(doc, region []byte) ([]byte, error) {
	start, end, err := regionBounds(doc)
	if err != nil {
		return nil, err
	}
	if start < 0 {
		if len(bytes.TrimSpace(doc)) == 0 {
			return region, nil
		}
		var out bytes.Buffer
		out.Write(doc)
		if !bytes.HasSuffix(doc, []byte("\n")) {
			out.WriteString("\n")
		}
		out.WriteString("\n")
		out.Write(region)
		return out.Bytes(), nil
	}
	var out bytes.Buffer
	out.Write(doc[:start])
	out.Write(region)
	out.Write(doc[end:])
	return out.Bytes(), nil
}

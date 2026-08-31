package skills

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
)

// This file holds one mechanism: the digest a projection's kernel-owned
// region carries over its own bytes, and the check that recomputes it.
//
// The source digests next door answer "has an input moved since this was
// generated". They cannot answer "is this still what was generated",
// because nothing about them depends on the region's own text — and that
// second question is the one that matters most for AGENTS.md, the file
// a harness actually reads. A region asserting in its own header that it
// is kernel-owned, with nothing verifying the assertion, is a claim the
// repository cannot back.

// regionHead is the opening of a rendered region: the begin marker and
// the one-line notice explaining why the file looks generated. The
// region digest line goes immediately after it, so its position is a
// constant a reader can point at rather than something anyone has to
// search for.
//
// The notice is not fixed — a repository projection names `pika skills
// install` and a global agent file names the `--global` form that wrote
// it — so the head is computed. Its length is what the digest splice
// depends on, which is why withRegionDigest is handed the head the core
// was rendered with instead of assuming one.
func regionHead(notice string) string { return beginMarker + "\n" + notice + "\n" }

// regionDigestNote spells out the one thing a self-referential digest
// has to say: what it covers. A digest recorded inside the bytes it
// covers cannot cover its own line, and the scheme is written on the
// line itself rather than left for a reader to infer — the alternative
// is a number nobody can check by hand.
const regionDigestNote = "(covers this region excluding this line)"

// regionDigestFmt renders that line; regionDigestLine reads it back.
// The pattern is exact apart from the hex, so a note somebody reworded
// is no longer a digest line at all and the region fails closed rather
// than being verified against a rule it no longer states.
const regionDigestFmt = "<!-- pika:region %s " + regionDigestNote + " -->"

var regionDigestLine = regexp.MustCompile(`^<!-- pika:region (sha256:[0-9a-f]{64}) ` + regexp.QuoteMeta(regionDigestNote) + ` -->$`)

// withRegionDigest returns core with its digest line spliced in
// directly after head, which is where every reader and
// splitRegionDigest expect to find it.
func withRegionDigest(core []byte, head, digest string) []byte {
	line := fmt.Sprintf(regionDigestFmt, digest) + "\n"
	out := make([]byte, 0, len(core)+len(line))
	out = append(out, core[:len(head)]...)
	out = append(out, line...)
	out = append(out, core[len(head):]...)
	return out
}

// splitRegionDigest separates a region on disk into the bytes its
// recorded digest is supposed to cover and the digest it records.
//
// It is the exact inverse of withRegionDigest: removing the one digest
// line, and the newline that ends it, reproduces the core the digest
// was taken over. Anything other than exactly one digest line is an
// error rather than a tolerated shape — a region with none records no
// claim about its own bytes at all, and a region with two does not say
// which claim it is making. The digest is an integrity check, not a
// signature: it detects change, and does not pretend to identify who
// made it.
func splitRegionDigest(region []byte) (core []byte, digest string, err error) {
	lines := bytes.Split(region, []byte("\n"))
	kept := make([][]byte, 0, len(lines))
	found := 0
	for _, line := range lines {
		m := regionDigestLine.FindSubmatch(bytes.TrimRight(line, "\r"))
		if m == nil {
			kept = append(kept, line)
			continue
		}
		found++
		digest = string(m[1])
	}
	switch found {
	case 1:
		return bytes.Join(kept, []byte("\n")), digest, nil
	case 0:
		return nil, "", fmt.Errorf("%w: it carries no `pika:region` digest line, so there is nothing to check its own bytes against", errUnverifiable)
	default:
		return nil, "", fmt.Errorf("%w: it carries %d `pika:region` digest lines, so it does not say which one describes it", errUnverifiable, found)
	}
}

// errUnverifiable marks a region the kernel cannot check at all, as
// opposed to one it checked and found changed. Both fail, and both risk
// the same thing when regenerated, but only one of them is evidence
// that somebody typed something: saying "you edited this" about a
// region whose digest line is simply absent would be the kernel
// asserting a fact it does not have.
var errUnverifiable = errors.New("cannot be verified")

// verifyRegion reports whether a region on disk still hashes to the
// digest it carries. This is the check that makes the kernel-owned
// region kernel-owned in fact and not only in the notice at the top of
// it: a hand edit changes the bytes, the recorded digest does not
// follow, and the mismatch is visible without consulting any source.
//
// That independence is the point. Inferring a hand edit by elimination —
// the region differs and no source moved — hides one the moment a
// source moves too, and reports the case where an operator's edit is
// about to be destroyed as the case where nothing is at risk.
//
// A returned error wrapping errUnverifiable means the kernel could not
// perform the check; any other error means it performed it and the
// region failed.
func verifyRegion(region []byte) error {
	core, want, err := splitRegionDigest(region)
	if err != nil {
		return err
	}
	if got := digestOf(core); got != want {
		return fmt.Errorf("records region digest %s but its bytes now hash to %s", want, got)
	}
	return nil
}

// tamperedDetail is what a region that failed its own digest check is
// told. It names the remedy the operator actually needs, which is not
// the one that fixes a stale copy: regenerating overwrites the edit
// rather than moving it, so the message says so and points at where the
// change belongs instead. Both halves come from the target's origin,
// because a global agent file has no canonical skill to be redirected to
// and being sent to one would be advice that cannot be followed.
//
// The lead sentence follows the evidence. A digest that was checked and
// disagreed proves somebody changed those bytes; a digest line that is
// missing or doubled proves only that the kernel cannot check. Both
// fail and both risk the same loss on regenerate, so both are tampered
// — but only the first is reported as a hand edit, because the second
// would be the kernel asserting a fact it does not have.
func tamperedDetail(err error, from origin) string {
	lead := "was edited by hand inside the pika skills markers: it " + err.Error()
	if errors.Is(err, errUnverifiable) {
		lead = "holds a kernel-owned region that " + err.Error() +
			", so the kernel cannot tell whether it still says what it generated"
	}
	return lead +
		"; that region is kernel-owned, and " + from.command + " would DISCARD whatever is there rather than keep it — " +
		from.source
}

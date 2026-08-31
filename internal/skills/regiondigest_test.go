package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Choaterboater/pika/internal/profiles"
)

// These tests cover the region's digest over its own bytes: that the
// scheme is a fixed point, that a hand edit inside the markers fails on
// the region's own evidence rather than by elimination, that operator
// prose outside them is neither digested nor checked, and that a region
// the kernel cannot locate or verify fails closed.

// lastReplace replaces the LAST occurrence of old in s. It exists
// because project-maintain's own skill text quotes the marker syntax
// inline to explain it — a real, deliberate use of prose naming the
// mechanism — so the marker string appears more than once in a
// composed region and a first-match strings.Replace can no longer be
// trusted to find the actual structural marker these fixtures mean to
// edit around.
func lastReplace(s, old, new string) string {
	i := strings.LastIndex(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}

// A region with no digest line is one the kernel cannot check, and it
// must fail on exactly that ground. Calling it a hand edit would assert
// something the kernel has no evidence for, and blaming a source would
// send the operator to a file that never changed.
func TestRegionWithNoDigestLineIsRefusedAsUnverifiable(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	target := filepath.Join(root.Dir(), "AGENTS.md")
	if err := os.WriteFile(target, []byte(beginMarker+"\nSomething somebody typed.\n"+endMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(root, c, resolved)
	if err != nil {
		t.Fatal(err)
	}
	p := st.Projections[0]
	if p.State != StateTampered {
		t.Fatalf("state = %s, want %s", p.State, StateTampered)
	}
	if !strings.Contains(p.Detail, "cannot be verified") {
		t.Errorf("detail = %q, want the unverifiable ground", p.Detail)
	}
	if strings.Contains(p.Detail, "edited by hand") {
		t.Errorf("detail claims an edit the kernel cannot have observed: %q", p.Detail)
	}
	if !strings.Contains(p.Detail, "DISCARD") {
		t.Errorf("detail does not warn that regenerating destroys what is there: %q", p.Detail)
	}
}

// The region digest is recorded inside the bytes it covers, so it cannot
// cover its own line. The scheme is therefore "everything in the region
// except the digest line", and the property that makes it usable is that
// it is a fixed point: rendering, checking, and rendering again produce
// identical bytes. A scheme that shifted on every regenerate would make
// every projection permanently drifted.
func TestRegionDigestIsStableAcrossARegenerateWithNoChanges(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	target := filepath.Join(root.Dir(), "AGENTS.md")

	if _, err := Install(root, c, resolved, false); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, target)
	if err := Verify(root.Dir(), c, resolved); err != nil {
		t.Fatalf("Verify after the first install = %v, want nil", err)
	}
	st, err := Install(root, c, resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	second := readFile(t, target)
	if first != second {
		t.Errorf("regenerating an unchanged projection changed the file:\n--- first\n%s\n--- second\n%s", first, second)
	}
	if st.Projections[0].Written {
		t.Error("regenerating an unchanged projection rewrote the file")
	}
	if err := Verify(root.Dir(), c, resolved); err != nil {
		t.Fatalf("Verify after regenerating = %v, want nil", err)
	}
}

// The digest line has to say what it covers, or it is a number nobody
// can check by hand — and it has to sit where the reader is told it
// sits, directly under the notice, so its position is a constant rather
// than a search.
func TestRegionDigestLineDeclaresItsOwnScheme(t *testing.T) {
	b := newBody(nil, nil, repoOrigin)
	lines := strings.Split(string(b.region), "\n")
	if len(lines) < 3 {
		t.Fatalf("region is too short to have a header:\n%s", b.region)
	}
	if lines[0] != beginMarker || lines[1] != repoOrigin.notice() {
		t.Fatalf("region head is not the documented two lines:\n%s", strings.Join(lines[:2], "\n"))
	}
	m := regionDigestLine.FindStringSubmatch(lines[2])
	if m == nil {
		t.Fatalf("the third line of a region is not its digest line: %q", lines[2])
	}
	if m[1] != b.digest {
		t.Errorf("recorded digest %s is not the one the body reports (%s)", m[1], b.digest)
	}
	if !strings.Contains(lines[2], "excluding this line") {
		t.Errorf("the digest line does not state that it excludes itself: %q", lines[2])
	}
}

// splitRegionDigest is the exact inverse of withRegionDigest: the bytes
// it hands back are the ones the digest was taken over. If the two ever
// disagreed, every projection would report itself tampered.
func TestSplitRegionDigestInvertsTheRender(t *testing.T) {
	b := newBody([]canonical{{
		name:   "demo",
		rel:    ".agents/skills/demo/SKILL.md",
		body:   []byte("---\nname: demo\n---\n\n# Title\n\nText.\n"),
		digest: digestOf([]byte("x")),
	}}, []profiles.GuidanceSet{{Ref: "go@1", Lines: []string{"run gofmt"}}}, repoOrigin)
	core, digest, err := splitRegionDigest(b.region)
	if err != nil {
		t.Fatal(err)
	}
	if digest != b.digest {
		t.Errorf("digest = %s, want %s", digest, b.digest)
	}
	if got := string(core); got != string(b.renderCore()) {
		t.Errorf("stripping the digest line did not reproduce the rendered core:\n%s", got)
	}
	if err := verifyRegion(b.region); err != nil {
		t.Errorf("a freshly rendered region does not verify: %v", err)
	}
}

// A hand edit inside the markers must be caught by the region's own
// digest, without consulting any source. This is the direction the
// source digests alone never covered: the file the harness actually
// reads is the one whose bytes were unverified.
func TestHandEditedRegionIsReportedAsTampered(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	if _, err := Install(root, c, resolved, false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root.Dir(), "AGENTS.md")
	doc := readFile(t, target)
	edited := lastReplace(doc, endMarker, "A line the kernel never issued.\n"+endMarker)
	if edited == doc {
		t.Fatal("fixture did not find the end marker")
	}
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(root, c, resolved)
	if err != nil {
		t.Fatal(err)
	}
	p := st.Projections[0]
	if p.State != StateTampered {
		t.Fatalf("state = %s, want %s (detail %q)", p.State, StateTampered, p.Detail)
	}
	// The detail must not stop at "these two digests differ": it must
	// point at the actual inserted line, the one difference version
	// control would otherwise be the only way to find.
	if !strings.Contains(p.Detail, "A line the kernel never issued.") {
		t.Errorf("detail does not attribute the tamper to the actual edited line: %q", p.Detail)
	}
	if !strings.Contains(p.Detail, "first difference is at line") {
		t.Errorf("detail does not localize the edit to a line: %q", p.Detail)
	}
	if !strings.Contains(p.Detail, "DISCARD") || !strings.Contains(p.Detail, ".agents/skills/") {
		t.Errorf("detail does not warn that regenerating destroys the edit, nor name where it belongs: %q", p.Detail)
	}
	err = Verify(root.Dir(), c, resolved)
	if err == nil {
		t.Fatal("Verify = nil on a tampered projection")
	}
	if !strings.Contains(err.Error(), StateTampered) {
		t.Errorf("gate 1 does not name the failure as tampered: %v", err)
	}
}

// Tampering must stay visible when a source moved in the same working
// tree. Inferring the hand edit by elimination hid exactly this case,
// and reported the one situation where an operator's work is about to be
// destroyed as the one where nothing is at risk.
func TestTamperIsNotMaskedByAMovedSource(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	if _, err := Install(root, c, resolved, false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root.Dir(), "AGENTS.md")
	doc := readFile(t, target)
	if err := os.WriteFile(target, []byte(lastReplace(doc, endMarker, "Mine.\n"+endMarker)), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := root.Skill("project-work")
	body, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, append(body, []byte("\nOne more rule.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Inspect(root, c, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Projections[0].State; got != StateTampered {
		t.Fatalf("state = %s, want %s: a moved source masked the hand edit (detail %q)", got, StateTampered, st.Projections[0].Detail)
	}
}

// Everything outside the markers is the operator's. AGENTS.md in this
// repository carries real operator prose above the region and `pika
// init` scaffolds more, so digesting it would make an ordinary edit to
// one's own file a gate failure. Both halves of the same file are
// checked here, because the value of each claim is that the other one
// does not hold.
func TestOperatorProseOutsideTheMarkersIsFreeAndInsideIsNot(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	target := filepath.Join(root.Dir(), "AGENTS.md")
	const preamble = "# My repository\n\nHouse rules live here.\n\n"
	if err := os.WriteFile(target, []byte(preamble), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(root, c, resolved, false); err != nil {
		t.Fatal(err)
	}

	doc := readFile(t, target)
	outside := strings.Replace(doc, "House rules live here.", "House rules live here, and I have just reworded them.", 1)
	if outside == doc {
		t.Fatal("fixture did not find the operator prose")
	}
	outside += "\nAnd a note appended below the region.\n"
	if err := os.WriteFile(target, []byte(outside), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Inspect(root, c, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if st.Projections[0].State != StateCurrent {
		t.Fatalf("editing the operator's own prose was reported as %s: %q", st.Projections[0].State, st.Projections[0].Detail)
	}
	if err := Verify(root.Dir(), c, resolved); err != nil {
		t.Fatalf("Verify = %v after an edit outside the markers, want nil", err)
	}

	inside := lastReplace(outside, endMarker, "One word inside.\n"+endMarker)
	if err := os.WriteFile(target, []byte(inside), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = Inspect(root, c, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if st.Projections[0].State != StateTampered {
		t.Fatalf("editing inside the markers was reported as %s, want %s", st.Projections[0].State, StateTampered)
	}
}

// A region the kernel cannot locate or cannot check must fail closed.
// Reporting `current` because the digest line is gone would let the
// simplest possible tamper — delete the line that records the digest —
// buy silence.
func TestDamagedRegionsFailClosed(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	target := filepath.Join(root.Dir(), "AGENTS.md")
	if _, err := Install(root, c, resolved, false); err != nil {
		t.Fatal(err)
	}
	good := readFile(t, target)
	digestLine := strings.Split(good, "\n")[strings.Count(good[:strings.Index(good, beginMarker)], "\n")+2]
	if regionDigestLine.FindStringSubmatch(digestLine) == nil {
		t.Fatalf("fixture did not find the digest line, got %q", digestLine)
	}

	for _, tc := range []struct {
		name  string
		doc   string
		state string
	}{
		{"digest line deleted", strings.Replace(good, digestLine+"\n", "", 1), StateTampered},
		{"digest line doubled", strings.Replace(good, digestLine+"\n", digestLine+"\n"+digestLine+"\n", 1), StateTampered},
		{"digest note reworded", strings.Replace(good, "excluding this line", "excluding this line!", 1), StateTampered},
		{"end marker deleted", strings.Replace(good, endMarker+"\n", "", 1), StateUnreadable},
		{"begin marker deleted", strings.Replace(good, beginMarker+"\n", "", 1), StateUnreadable},
		{"region duplicated", good + "\n" + good, StateUnreadable},
		{"markers reordered", lastReplace(strings.Replace(good, beginMarker, "\x00", 1), endMarker, beginMarker), StateUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := strings.ReplaceAll(tc.doc, "\x00", endMarker)
			if err := os.WriteFile(target, []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
			st, err := Inspect(root, c, resolved)
			if err != nil {
				t.Fatal(err)
			}
			if got := st.Projections[0].State; got != tc.state {
				t.Errorf("state = %s, want %s (detail %q)", got, tc.state, st.Projections[0].Detail)
			}
			if st.OK {
				t.Error("a region the kernel cannot check reported ok")
			}
			if err := Verify(root.Dir(), c, resolved); err == nil {
				t.Error("Verify = nil; a damaged region passed gate 1")
			}
		})
	}
}

// An install is entitled to overwrite a hand-edited region, but not to
// do it silently: the operator learns their words are gone from the
// command that removed them, not from a diff a week later.
func TestInstallReportsThatItDiscardedAHandEdit(t *testing.T) {
	root := rootAt(t)
	c, resolved := codexContract(), resolve(t, profiles.CoreRef)
	target := filepath.Join(root.Dir(), "AGENTS.md")
	if _, err := Install(root, c, resolved, false); err != nil {
		t.Fatal(err)
	}
	doc := readFile(t, target)
	if err := os.WriteFile(target, []byte(lastReplace(doc, endMarker, "Mine.\n"+endMarker)), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Install(root, c, resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	p := st.Projections[0]
	if p.State != StateCurrent || !p.Written {
		t.Fatalf("install did not regenerate the region: %+v", p)
	}
	if !strings.Contains(p.Detail, "replaced a hand-edited kernel-owned region") {
		t.Errorf("install did not report what it discarded: %q", p.Detail)
	}
	if readFile(t, target) != doc {
		t.Error("regenerating did not restore the kernel's own bytes")
	}
}

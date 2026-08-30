package lease

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// deadPID is a pid no process is running under, so a lease naming it
// stands in for a holder that died without releasing.
const deadPID = 99999999

// A second holder is the whole point: the filesystem decides the winner
// and the loser is told who won rather than proceeding beside them.
func TestAcquireExcludesASecondHolder(t *testing.T) {
	dir := t.TempDir()
	if _, state, err := Inspect(dir, "lock"); state != StateFree || err != nil {
		t.Fatalf("Inspect on a fresh dir = %v, %v; want free", state, err)
	}

	h, err := Acquire(dir, "lock", Info{ID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir, "lock", Info{ID: "second"})
	if second != nil {
		t.Fatal("a second holder acquired a lease that was already held")
	}
	var he *HeldError
	if !errors.As(err, &he) {
		t.Fatalf("second acquire = %v, want *HeldError", err)
	}
	if he.Info == nil || he.Info.ID != "first" {
		t.Errorf("HeldError.Info = %+v, want the first holder named", he.Info)
	}
	if he.State != StateHeld {
		t.Errorf("state = %v, want held: this process is the holder and is running", he.State)
	}
	if _, err := os.Lstat(Path(dir, "lock")); err != nil {
		t.Errorf("lease file after a refused acquire: %v, want it untouched", err)
	}

	// Released, the name is free again.
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	if _, state, err := Inspect(dir, "lock"); state != StateFree || err != nil {
		t.Fatalf("Inspect after Release = %v, %v; want free", state, err)
	}
	third, err := Acquire(dir, "lock", Info{ID: "third"})
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatal(err)
	}
}

// Removing a lease that is no longer yours is stealing it, whatever the
// handle in hand says. Release proves ownership from disk first.
func TestReleaseByNonHolderRefuses(t *testing.T) {
	dir := t.TempDir()
	mine, err := Acquire(dir, "lock", Info{ID: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	// The lease turns over: this handle's claim ends and another
	// process takes the name.
	if err := os.Remove(Path(dir, "lock")); err != nil {
		t.Fatal(err)
	}
	theirs, err := Acquire(dir, "lock", Info{ID: "theirs"})
	if err != nil {
		t.Fatal(err)
	}

	if err := mine.Release(); err == nil {
		t.Error("Release removed a lease held by somebody else")
	} else if !strings.Contains(err.Error(), "theirs") {
		t.Errorf("Release error = %q, want it to name the current holder", err)
	}
	info, state, err := Inspect(dir, "lock")
	if err != nil || info == nil || info.ID != "theirs" || state != StateHeld {
		t.Fatalf("lease after refusal = %+v, %v, %v; want it still held by theirs", info, state, err)
	}

	// The real holder still releases.
	if err := theirs.Release(); err != nil {
		t.Fatal(err)
	}

	// An already-absent lease is the state the caller wanted.
	if err := theirs.Release(); err != nil {
		t.Errorf("Release of an absent lease = %v, want nil", err)
	}
}

// A holder recorded on this host whose process is gone is the one state
// in which an operator may clear the lease.
func TestDeadHolderOnThisHostIsStale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("processAlive cannot prove a holder dead on Windows")
	}
	dir := t.TempDir()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir, "lock", Info{ID: "crashed", PID: deadPID, Host: host}); err != nil {
		t.Fatal(err)
	}
	info, state, err := Inspect(dir, "lock")
	if err != nil {
		t.Fatal(err)
	}
	if state != StateStale {
		t.Errorf("state = %v, want stale: the holder is on this host and is not running", state)
	}
	if info == nil || info.PID != deadPID || info.Host != host {
		t.Errorf("info = %+v, want the dead holder on %s reported", info, host)
	}
	// Stale is a report, not a repossession: the lease is still there
	// and still excludes.
	if _, err := Acquire(dir, "lock", Info{ID: "next"}); err == nil {
		t.Error("a stale lease was stolen automatically")
	}
	if _, err := os.Lstat(Path(dir, "lock")); err != nil {
		t.Errorf("lease file after Inspect: %v, want it untouched", err)
	}
}

// The one that matters most. A pid is only meaningful on the machine
// that recorded it: on a shared filesystem, "not running here" says
// nothing about the holder, and reporting it stale invites an operator
// to clear a lock a live process still holds.
func TestForeignHostIsUnverifiableNotStale(t *testing.T) {
	dir := t.TempDir()
	if _, err := Acquire(dir, "lock", Info{ID: "elsewhere", PID: deadPID, Host: "another-machine.invalid"}); err != nil {
		t.Fatal(err)
	}
	info, state, err := Inspect(dir, "lock")
	if err != nil {
		t.Fatal(err)
	}
	if state == StateStale {
		t.Fatal("a holder on another host was reported stale; clearing it would put two writers in one tree")
	}
	if state != StateUnverifiable {
		t.Errorf("state = %v, want unverifiable", state)
	}
	if info == nil || info.Host != "another-machine.invalid" {
		t.Errorf("info = %+v, want the foreign host reported so an operator can go and look", info)
	}
	var he *HeldError
	if _, err := Acquire(dir, "lock", Info{ID: "here"}); !errors.As(err, &he) {
		t.Errorf("acquire against a foreign holder = %v, want *HeldError", err)
	} else if he.State != StateUnverifiable {
		t.Errorf("HeldError.State = %v, want unverifiable", he.State)
	}
}

// The exclusion is only worth anything if it holds under a real race,
// so this runs one: N goroutines, one name, exactly one winner.
func TestAcquireIsAtomicUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	const n = 16

	var wg sync.WaitGroup
	start := make(chan struct{})
	handles := make(chan *Handle, n)
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			h, err := Acquire(dir, "lock", Info{ID: "racer-" + strconv.Itoa(i)})
			if err != nil {
				errs <- err
				return
			}
			handles <- h
		}()
	}
	close(start)
	wg.Wait()
	close(handles)
	close(errs)

	var winners []*Handle
	for h := range handles {
		winners = append(winners, h)
	}
	if len(winners) != 1 {
		t.Fatalf("%d goroutines acquired the lease, want exactly 1", len(winners))
	}
	for err := range errs {
		var he *HeldError
		if !errors.As(err, &he) {
			t.Errorf("losing acquire = %v, want *HeldError", err)
		}
	}
	// The winner is the holder on disk. A loser may read the file in
	// the window between the O_EXCL create and the fsynced write, so
	// its HeldError need not name a holder — but the file that survives
	// must.
	info, state, err := Inspect(dir, "lock")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.ID != winners[0].info.ID || state != StateHeld {
		t.Errorf("holder on disk = %+v (%v), want the winner %s", info, state, winners[0].info.ID)
	}
	if err := winners[0].Release(); err != nil {
		t.Fatal(err)
	}
}

// A lease written before this package existed spells its id "txId". A
// repository wedged by one of those must still be diagnosable.
func TestLegacyRecordIsReadable(t *testing.T) {
	dir := t.TempDir()
	body := `{"txId":"0000000000000001-c0ffee01","pid":` + strconv.Itoa(os.Getpid()) + `,"startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "lock"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	info, state, err := Inspect(dir, "lock")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || info.ID != "0000000000000001-c0ffee01" {
		t.Fatalf("info = %+v, want the legacy id read", info)
	}
	if !info.StartedAt.Equal(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("StartedAt = %v, want the recorded time", info.StartedAt)
	}
	// No host recorded: judged locally, because that is what every such
	// lock on disk is.
	if state != StateHeld {
		t.Errorf("state = %v, want held: the recorded pid is this running process", state)
	}
}

// A lease claimed by a process that died before writing its holder into
// it names nobody. It cannot be proved stale, and nothing pretends it is
// free.
func TestUnwrittenLeaseNamesNoHolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	info, state, err := Inspect(dir, "lock")
	if err == nil {
		t.Fatal("Inspect of an empty lease = nil error, want the read failure reported")
	}
	if info != nil || state != StateUnverifiable {
		t.Errorf("Inspect = %+v, %v; want no holder and unverifiable", info, state)
	}
	var he *HeldError
	if _, err := Acquire(dir, "lock", Info{ID: "next"}); !errors.As(err, &he) {
		t.Fatalf("acquire = %v, want *HeldError", err)
	}
	if he.Info != nil {
		t.Errorf("HeldError.Info = %+v, want nil: the lease names nobody", he.Info)
	}
}

// Clear is the one sweep, and what it refuses is the whole point of
// having it in one place: a live holder is a process in the tree, and a
// foreign holder is a pid this machine cannot judge at all. Only a
// holder proved gone on this host is removed.
func TestClearSweepsOnlyAProvablyDeadHolder(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		info  Info
		state State
	}{
		{"live", Info{ID: "running", PID: os.Getpid(), Host: host}, StateHeld},
		{"foreign", Info{ID: "elsewhere", PID: deadPID, Host: "another-machine.invalid"}, StateUnverifiable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Acquire(dir, "lock", tc.info); err != nil {
				t.Fatal(err)
			}
			var he *HeldError
			cleared, err := Clear(dir, "lock")
			if cleared {
				t.Fatalf("Clear removed a %v lease", tc.state)
			}
			if !errors.As(err, &he) {
				t.Fatalf("Clear of a %v lease = %v, want *HeldError", tc.state, err)
			}
			if he.State != tc.state {
				t.Errorf("HeldError.State = %v, want %v", he.State, tc.state)
			}
			if _, err := os.Lstat(Path(dir, "lock")); err != nil {
				t.Errorf("lease file after a refused Clear: %v, want it untouched", err)
			}
		})
	}

	t.Run("free", func(t *testing.T) {
		dir := t.TempDir()
		cleared, err := Clear(dir, "lock")
		if cleared || err != nil {
			t.Errorf("Clear of a free name = %v, %v; want false and no error: that is the state the caller wanted", cleared, err)
		}
	})

	t.Run("stale", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("processAlive cannot prove a holder dead on Windows")
		}
		dir := t.TempDir()
		if _, err := Acquire(dir, "lock", Info{ID: "crashed", PID: deadPID, Host: host}); err != nil {
			t.Fatal(err)
		}
		cleared, err := Clear(dir, "lock")
		if !cleared || err != nil {
			t.Fatalf("Clear of a stale lease = %v, %v; want it swept", cleared, err)
		}
		if _, err := os.Lstat(Path(dir, "lock")); !os.IsNotExist(err) {
			t.Errorf("lease file after Clear: %v, want it gone", err)
		}
		// And the name is usable again, which is the point.
		if _, err := Acquire(dir, "lock", Info{ID: "next"}); err != nil {
			t.Errorf("Acquire after Clear: %v, want the name free", err)
		}
	})
}

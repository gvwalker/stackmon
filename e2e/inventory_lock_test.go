package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The inventory is one file that every writer replaces whole. Two commands
// that read it, change it, and write it back with nothing between them lose
// one of the two changes: the last write wins, and the other change is gone
// without a word. These scenarios run two commands at once and check that
// both changes survive.
//
// The inventory transaction announces where it is, so a scenario can hold one
// open and watch that the next writer waits for it: the first command is
// parked after it has loaded the inventory, the second one finds the lock
// held, and only then is the first one let through.

// stackBody is a compose file for a stack a scenario enrolls. The image is
// never pulled: these scenarios are about the inventory, not the images.
const stackBody = "services:\n  api:\n    image: nginx:1.27\n"

// stackDir writes a stack's compose file and returns its directory, which is
// what a path enrollment takes.
func (e *env) stackDir(name string) string {
	e.t.Helper()
	return filepath.Dir(e.stack(name, stackBody))
}

// event is one announcement of where a command's transaction is.
func event(kind string, p *pending) string {
	return fmt.Sprintf("%s %d", kind, p.pid())
}

// Two enrollments at once must both land. The second one waits for the
// first, and then loads what the first wrote, so its own change does not
// come at the price of the first one's.
func TestConcurrentEnrollmentsBothSurvive(t *testing.T) {
	e := newEnv(t)
	barrier := e.inventoryBarrier()
	alpha := e.stackDir("alpha")
	beta := e.stackDir("beta")

	one := e.startWith(barrier.parkEnv(), "enroll", alpha)
	barrier.waitFor(t, "the first enrollment to load the inventory", func(ev string) bool {
		return ev == event("load", one)
	})

	two := e.startWith(barrier.eventsEnv(), "enroll", beta)
	barrier.waitFor(t, "the second enrollment to find the inventory locked", func(ev string) bool {
		return ev == event("contended", two)
	})
	barrier.release(t)

	first := one.wait(t)
	assertCode(t, "first enrollment", first.code, 0, first)
	second := two.wait(t)
	assertCode(t, "second enrollment", second.code, 0, second)

	// The second writer only started its transaction once the first one had
	// written, so it loaded what the first one wrote.
	barrier.order(t, event("save", one), event("lock", two))
	assertEqSlice(t, "enrolled stacks", e.enrolledNames(), []string{"alpha", "beta"})

	// The inventory did not exist before the two commands ran: the lock
	// covers the transaction that creates it too.
	assertEqSlice(t, "the state directory", e.stateDirEntries(), []string{"inventory.json", "inventory.json.lock"})
	e.note("inventory: %s", e.normalize(e.inventoryJSON()))
}

// Two enrollments of the same stack at once cannot both succeed: the second
// one loads what the first one wrote and finds the name taken.
func TestSimultaneousDuplicateEnrollmentCannotBothSucceed(t *testing.T) {
	e := newEnv(t)
	barrier := e.inventoryBarrier()
	alpha := e.stackDir("alpha")

	one := e.startWith(barrier.parkEnv(), "enroll", alpha)
	barrier.waitFor(t, "the first enrollment to load the inventory", func(ev string) bool {
		return ev == event("load", one)
	})

	two := e.startWith(barrier.eventsEnv(), "enroll", alpha)
	barrier.waitFor(t, "the second enrollment to find the inventory locked", func(ev string) bool {
		return ev == event("contended", two)
	})
	barrier.release(t)

	first := one.wait(t)
	assertCode(t, "first enrollment", first.code, 0, first)
	second := two.wait(t)
	assertCode(t, "second enrollment", second.code, 1, second)
	assertContains(t, "second enrollment", second.stderr, "error:", "already enrolled")

	assertEqSlice(t, "enrolled stacks", e.enrolledNames(), []string{"alpha"})
	e.note("inventory: %s", e.normalize(e.inventoryJSON()))
}

// A binding and an enrollment at once must both land: the binding is not
// lost because an enrollment replaced the inventory, and the enrollment is
// not lost because a binding replaced the inventory.
func TestEnrollmentAndBindingDoNotDiscardEachOther(t *testing.T) {
	e := newEnv(t)
	barrier := e.inventoryBarrier()
	e.enrollStack("alpha", stackBody)
	beta := e.stackDir("beta")

	one := e.startWith(barrier.parkEnv(), "enroll", beta)
	barrier.waitFor(t, "the enrollment to load the inventory", func(ev string) bool {
		return ev == event("load", one)
	})

	two := e.startWith(barrier.eventsEnv(), "inventory", "set-project", "alpha", "prod")
	barrier.waitFor(t, "the binding to find the inventory locked", func(ev string) bool {
		return ev == event("contended", two)
	})
	barrier.release(t)

	first := one.wait(t)
	assertCode(t, "the enrollment", first.code, 0, first)
	second := two.wait(t)
	assertCode(t, "the binding", second.code, 0, second)

	barrier.order(t, event("save", one), event("lock", two))
	assertEqSlice(t, "enrolled stacks", e.enrolledNames(), []string{"alpha", "beta"})
	assertEq(t, "bound project", e.boundProject("alpha"), "prod")
	e.note("inventory: %s", e.normalize(e.inventoryJSON()))
}

// An unenrollment and an enrollment at once must both land: the stack that
// was unenrolled stays unenrolled, and the one that was enrolled stays
// enrolled.
func TestEnrollmentAndUnenrollmentDoNotDiscardEachOther(t *testing.T) {
	e := newEnv(t)
	barrier := e.inventoryBarrier()
	e.enrollStack("alpha", stackBody)
	beta := e.stackDir("beta")

	one := e.startWith(barrier.parkEnv(), "enroll", beta)
	barrier.waitFor(t, "the enrollment to load the inventory", func(ev string) bool {
		return ev == event("load", one)
	})

	two := e.startWith(barrier.eventsEnv(), "unenroll", "alpha")
	barrier.waitFor(t, "the unenrollment to find the inventory locked", func(ev string) bool {
		return ev == event("contended", two)
	})
	barrier.release(t)

	first := one.wait(t)
	assertCode(t, "the enrollment", first.code, 0, first)
	second := two.wait(t)
	assertCode(t, "the unenrollment", second.code, 0, second)

	barrier.order(t, event("save", one), event("lock", two))
	assertEqSlice(t, "enrolled stacks", e.enrolledNames(), []string{"beta"})
	e.note("inventory: %s", e.normalize(e.inventoryJSON()))
}

// A refused change writes nothing: the inventory is left as it was, and the
// lock is released, so the next command runs.
func TestARefusedChangeLeavesTheInventoryAloneAndReleasesTheLock(t *testing.T) {
	e := newEnv(t)
	alpha := e.stackDir("alpha")
	beta := e.stackDir("beta")
	if r := e.enroll(alpha); r.code != 0 {
		t.Fatalf("enrolling alpha: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	before := e.inventoryJSON()

	again := e.enroll(alpha)
	assertCode(t, "the second enrollment", again.code, 1, again)
	assertContains(t, "the second enrollment", again.stderr, "error:", "already enrolled")
	assertEq(t, "the inventory", e.inventoryJSON(), before)

	// The lock was released: the next command runs.
	next := e.enroll(beta)
	assertCode(t, "the enrollment after the refusal", next.code, 0, next)
	assertEqSlice(t, "enrolled stacks", e.enrolledNames(), []string{"alpha", "beta"})

	// A refused change leaves nothing behind but the inventory and its lock.
	assertEqSlice(t, "the state directory", e.stateDirEntries(), []string{"inventory.json", "inventory.json.lock"})
	e.note("inventory: %s", e.normalize(e.inventoryJSON()))
}

// A write that cannot happen leaves the inventory as it was: the change is
// refused, and the lock is released so the next command can try again.
func TestAFailedWriteLeavesTheInventoryAloneAndReleasesTheLock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the mode bits below do not stop a write")
	}
	e := newEnv(t)
	alpha := e.stackDir("alpha")
	beta := e.stackDir("beta")
	if r := e.enroll(alpha); r.code != 0 {
		t.Fatalf("enrolling alpha: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	before := e.inventoryJSON()

	// The directory that holds the inventory cannot be written to any more.
	// The lock file is already there, so the transaction still starts.
	if err := os.Chmod(e.stateDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.stateDir(), 0o755) })

	failed := e.enroll(beta)
	assertCode(t, "the enrollment that cannot write", failed.code, 1, failed)
	assertContains(t, "the enrollment that cannot write", failed.stderr, "error:")
	assertEq(t, "the inventory", e.inventoryJSON(), before)

	// The lock was released: with the directory writable again, the next
	// command runs.
	if err := os.Chmod(e.stateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	next := e.enroll(beta)
	assertCode(t, "the enrollment after the failed write", next.code, 0, next)
	assertEqSlice(t, "enrolled stacks", e.enrolledNames(), []string{"alpha", "beta"})
	e.note("inventory: %s", e.normalize(e.inventoryJSON()))
}

// A command that dies holding the lock must not lock the inventory
// forever: the kernel drops the lock with the process.
func TestTheInventoryLockIsReleasedWhenTheProcessDies(t *testing.T) {
	e := newEnv(t)
	barrier := e.inventoryBarrier()
	alpha := e.stackDir("alpha")
	beta := e.stackDir("beta")

	one := e.startWith(barrier.parkEnv(), "enroll", alpha)
	barrier.waitFor(t, "the enrollment to load the inventory", func(ev string) bool {
		return ev == event("load", one)
	})

	// The next writer waits for the lock the first one holds.
	two := e.startWith(barrier.eventsEnv(), "enroll", beta)
	barrier.waitFor(t, "the second enrollment to find the inventory locked", func(ev string) bool {
		return ev == event("contended", two)
	})

	// The first command is killed while it holds the lock, with nothing
	// written.
	one.kill(t)
	killed := one.wait(t)
	assertCode(t, "the killed enrollment", killed.code, -1, killed)
	assertEq(t, "the inventory", e.inventoryJSON(), "")

	// The waiting writer does not wait for a lock nobody holds any more.
	second := two.wait(t)
	assertCode(t, "the enrollment after the crash", second.code, 0, second)
	assertEqSlice(t, "enrolled stacks", e.enrolledNames(), []string{"beta"})
	e.note("inventory: %s", e.normalize(e.inventoryJSON()))
}

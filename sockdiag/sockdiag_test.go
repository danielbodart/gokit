package sockdiag

import (
	"context"
	"testing"
)

func in(cgroup string) Listener { return Listener{Protocol: TCP, Port: 22, Cgroup: cgroup} }

func TestWithinTakesTheGroupItselfAndItsSubCgroups(t *testing.T) {
	assertEqual(t, in("/system.slice/sshd.service").Within("/system.slice/sshd.service"), true)
	assertEqual(t, in("/system.slice/app.service/worker").Within("/system.slice/app.service"), true)
	assertEqual(t, in("/system.slice/app.service/worker").Within("/system.slice/app.service/"), true)
	assertEqual(t, in("/system.slice/sshd.service").Within("/user.slice", "/system.slice/sshd.service"), true)
}

// foo.service2 shares foo.service's prefix but is a sibling, not a child.
func TestWithinLeavesOutASiblingWithALongerName(t *testing.T) {
	assertEqual(t, in("/system.slice/foo.service2").Within("/system.slice/foo.service"), false)
}

// A unit that is not running has no ControlGroup; it must claim no sockets,
// not every one.
func TestWithinMatchesNothingForAnUnknownCgroupOrAnEmptyGroup(t *testing.T) {
	assertEqual(t, in("").Within("/system.slice/sshd.service"), false)
	assertEqual(t, in("").Within("/"), false)
	assertEqual(t, in("/system.slice/sshd.service").Within(""), false)
	assertEqual(t, in("/system.slice/sshd.service").Within(), false)
}

func TestWithinTheRootIsEverything(t *testing.T) {
	assertEqual(t, in("/system.slice/sshd.service").Within("/"), true)
}

func TestListerFuncIsALister(t *testing.T) {
	want := []Listener{in("/system.slice/sshd.service")}
	var lister Lister = ListerFunc(func(context.Context) ([]Listener, error) { return want, nil })
	got, err := lister.Listening(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, got, want)
}

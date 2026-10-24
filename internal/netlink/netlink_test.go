package netlink

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"slices"
	"syscall"
	"testing"
	"time"
)

var ctx = context.Background()

func assertEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func attribute(kind uint16, payload []byte) []byte {
	length := syscall.SizeofRtAttr + len(payload)
	out := make([]byte, (length+3)&^3)
	binary.NativeEndian.PutUint16(out[0:], uint16(length))
	binary.NativeEndian.PutUint16(out[2:], kind)
	copy(out[syscall.SizeofRtAttr:], payload)
	return out
}

func message(kind uint16, seq uint32, data []byte) []byte {
	out := binary.NativeEndian.AppendUint32(nil, uint32(syscall.NLMSG_HDRLEN+len(data)))
	out = binary.NativeEndian.AppendUint16(out, kind)
	out = binary.NativeEndian.AppendUint16(out, syscall.NLM_F_MULTI)
	out = binary.NativeEndian.AppendUint32(out, seq)
	out = binary.NativeEndian.AppendUint32(out, 0)
	out = append(out, data...)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	return out
}

func done(seq uint32) []byte { return message(syscall.NLMSG_DONE, seq, make([]byte, 4)) }

func datagrams(all ...[]byte) func() ([]byte, error) {
	return func() ([]byte, error) {
		if len(all) == 0 {
			return nil, errors.New("read past the end of the dump")
		}
		next := all[0]
		all = all[1:]
		return next, nil
	}
}

func TestAttributesAreReadByTypeWithoutTheirFlags(t *testing.T) {
	found, err := Attributes(slices.Concat(
		attribute(1, []byte{7}),
		attribute(2|1<<15 /* NLA_F_NESTED */, attribute(1, []byte("x"))),
		attribute(3|1<<14 /* NLA_F_NET_BYTEORDER */, []byte{0, 80}),
		attribute(4, nil),
	))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, found, map[uint16][]byte{1: {7}, 2: attribute(1, []byte("x")), 3: {0, 80}, 4: {}})
}

func TestNoAttributesAreAnEmptyRun(t *testing.T) {
	found, err := Attributes(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, found, map[uint16][]byte{})
}

// The last attribute's padding may be left off, as the message's length
// already ends it.
func TestTheLastAttributeNeedNotBePadded(t *testing.T) {
	found, err := Attributes(attribute(1, []byte{7})[:5])
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, found, map[uint16][]byte{1: {7}})
}

func TestAttributesThatDoNotHoldTogetherAreAnError(t *testing.T) {
	overrun := attribute(1, []byte{7})
	binary.NativeEndian.PutUint16(overrun, 9)
	short := attribute(1, nil)
	binary.NativeEndian.PutUint16(short, 3)
	for name, data := range map[string][]byte{
		"a length past the end":      overrun,
		"a length shorter than 4":    short,
		"stray bytes after the last": append(attribute(1, []byte{7}), 1, 2),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Attributes(data); err == nil {
				t.Fatal("read them")
			}
		})
	}
}

func TestRequestIsADumpRequestWithItsBody(t *testing.T) {
	got := Request(20, 3, []byte{1, 2, 3, 4})
	order := binary.NativeEndian
	assertEqual(t, order.Uint32(got[0:]), 20)
	assertEqual(t, order.Uint16(got[4:]), 20)
	assertEqual(t, order.Uint16(got[6:]), syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)
	assertEqual(t, order.Uint32(got[8:]), 3)
	assertEqual(t, got[16:], []byte{1, 2, 3, 4})
}

func collected(t *testing.T, receive func() ([]byte, error)) ([]uint16, error) {
	t.Helper()
	var kinds []uint16
	err := Collect(ctx, receive, 1, func(message syscall.NetlinkMessage) error {
		kinds = append(kinds, message.Header.Type)
		return nil
	})
	return kinds, err
}

func TestCollectHandsOverEveryMessageOfTheDumpUntilDone(t *testing.T) {
	kinds, err := collected(t, datagrams(
		slices.Concat(message(20, 1, nil), message(syscall.NLMSG_NOOP, 1, nil), message(21, 2, nil)),
		slices.Concat(message(22, 1, nil), done(1), message(23, 1, nil)),
	))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, kinds, []uint16{20, 22})
}

func TestCollectStopsAtTheFirstRefusal(t *testing.T) {
	refused := errors.New("not one of mine")
	err := Collect(ctx, datagrams(slices.Concat(message(20, 1, nil), done(1))), 1, func(syscall.NetlinkMessage) error {
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("got %v", err)
	}
}

func TestAnOverrunIsAnError(t *testing.T) {
	if _, err := collected(t, datagrams(slices.Concat(message(syscall.NLMSG_OVERRUN, 1, nil), done(1)))); err == nil {
		t.Fatal("lost messages without saying so")
	}
}

// negated is an errno as the kernel sends it.
func negated(errno syscall.Errno) []byte {
	return binary.NativeEndian.AppendUint32(nil, uint32(-int32(errno)))
}

func TestTheKernelsErrnoIsTheError(t *testing.T) {
	refusal := message(syscall.NLMSG_ERROR, 1, negated(syscall.EPERM))
	if _, err := collected(t, datagrams(refusal)); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("got %v", err)
	}
	failed := message(syscall.NLMSG_DONE, 1, negated(syscall.EBUSY))
	if _, err := collected(t, datagrams(failed)); !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("got %v", err)
	}
}

func TestOpenRefusesAContextAlreadyPastItsDeadline(t *testing.T) {
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	if _, err := Open(expired, "test", syscall.NETLINK_ROUTE, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

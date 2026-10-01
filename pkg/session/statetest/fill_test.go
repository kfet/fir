package statetest

import (
	"fmt"
	"testing"
)

type fakeTB struct {
	testing.TB
	fails []string
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fails = append(f.fails, fmt.Sprintf(format, args...))
}

type special struct{ X int }

type all struct {
	S      string
	B      bool
	I      int
	F      float64
	MA     map[string]any
	MS     map[string]string
	L      []string
	N      struct{ Inner string }
	Sp     special
	hidden string
}

func TestFillNonZero_AllKinds(t *testing.T) {
	var v all
	FillNonZero(t, &v, "s", special{X: 1})
	if v.S != "s" || !v.B || v.I != 7 || v.F != 7 || v.MA["k"] != "v" || v.MS["k"] != "v" ||
		len(v.L) != 1 || v.N.Inner != "s" || v.Sp.X != 1 || v.hidden != "" {
		t.Fatalf("got %#v", v)
	}
}

func TestFillNonZero_Failures(t *testing.T) {
	var unsupported struct{ C chan int }
	tb := &fakeTB{}
	FillNonZero(tb, &unsupported, "s")
	if len(tb.fails) != 2 { // cannot fill, then left zero
		t.Fatalf("fails = %v", tb.fails)
	}

	var zero struct{ Sp special }
	tb = &fakeTB{}
	FillNonZero(tb, &zero, "s", special{})
	if len(tb.fails) != 1 {
		t.Fatalf("fails = %v", tb.fails)
	}
}

package pyfmt

import "testing"

func TestRepr(t *testing.T) {
	a, b := 0.1, 0.2
	for f, want := range map[float64]string{7: "7.0", 5.9: "5.9", a + b: "0.30000000000000004", 1e-5: "1e-05",
		1e16: "1e+16", 1234567.5: "1234567.5", 0.0001: "0.0001", -2.5: "-2.5", 123456789012345678: "1.2345678901234568e+17",
		8.43: "8.43", 100: "100.0"} {
		if got := Repr(f); got != want {
			t.Errorf("%v: %s, want %s", f, got, want)
		}
	}
}

func TestG(t *testing.T) {
	a, b := 0.1, 0.2
	for _, c := range []struct {
		f    float64
		p    int
		want string
	}{{3, 6, "3"}, {3.5, 6, "3.5"}, {a + b, 6, "0.3"}, {1234567, 6, "1.23457e+06"}, {0.00001, 6, "1e-05"},
		{8.4285714, 3, "8.43"}, {10, 3, "10"}, {-1.5, 6, "-1.5"}} {
		if got := G(c.f, c.p); got != c.want {
			t.Errorf("%v %d: %s, want %s", c.f, c.p, got, c.want)
		}
	}
}

func TestRound(t *testing.T) {
	for _, c := range []struct{ x, want float64 }{{2.675, 2.67}, {0.125, 0.12}, {0.375, 0.38}, {8.4285714, 8.43}} {
		if got := Round(c.x, 2); got != c.want {
			t.Errorf("%v: %v", c.x, got)
		}
	}
	if RoundInt(2.5) != 2 || RoundInt(3.5) != 4 {
		t.Error("half to even")
	}
}

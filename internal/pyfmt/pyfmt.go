// Package pyfmt writes numbers as the Python version of the bot wrote them: what it showed users and what it put
// into the prompts and cache keys of the layout must stay the same text.
package pyfmt

import (
	"math"
	"strconv"
	"strings"
)

// Repr is Python's str(float): the shortest digits, scientific below 1e-4 and from 1e16, always with a point or an
// exponent ("7.0", "5.9", "1e-05", "1e+16").
func Repr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	digits, exp := shortest(f)
	sign := ""
	if f < 0 || (f == 0 && math.Signbit(f)) {
		sign = "-"
	}
	if exp < -4 || exp >= 16 {
		mant := digits[:1]
		if len(digits) > 1 {
			mant += "." + digits[1:]
		}
		return sign + mant + expText(exp)
	}
	return sign + fixed(digits, exp, true)
}

// G is Python's format(f, "g") with a precision: 6 significant digits by default, no trailing zeros.
func G(f float64, precision int) string {
	if precision == 0 {
		precision = 1
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Repr(f)
	}
	if f == 0 {
		if math.Signbit(f) {
			return "-0"
		}
		return "0"
	}
	s := strconv.FormatFloat(math.Abs(f), 'e', precision-1, 64)
	mantissa, e, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(e)
	digits := strings.TrimRight(strings.Replace(mantissa, ".", "", 1), "0")
	if digits == "" {
		digits = "0"
	}
	sign := ""
	if f < 0 {
		sign = "-"
	}
	if exp < -4 || exp >= precision {
		mant := digits[:1]
		if len(digits) > 1 {
			mant += "." + digits[1:]
		}
		return sign + mant + expText(exp)
	}
	return sign + fixed(digits, exp, false)
}

// shortest gives the shortest decimal digits of |f| and the exponent of the first one.
func shortest(f float64) (string, int) {
	s := strconv.FormatFloat(math.Abs(f), 'e', -1, 64)
	mantissa, e, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(e)
	return strings.Replace(mantissa, ".", "", 1), exp
}

func expText(exp int) string {
	sign := "+"
	if exp < 0 {
		sign, exp = "-", -exp
	}
	n := strconv.Itoa(exp)
	if len(n) < 2 {
		n = "0" + n
	}
	return "e" + sign + n
}

// fixed writes digits d1d2d3... with the first at 10^exp; pointZero keeps ".0" on a whole number.
func fixed(digits string, exp int, pointZero bool) string {
	var whole, frac string
	switch {
	case exp < 0:
		whole, frac = "0", strings.Repeat("0", -exp-1)+digits
	case exp+1 >= len(digits):
		whole = digits + strings.Repeat("0", exp+1-len(digits))
	default:
		whole, frac = digits[:exp+1], digits[exp+1:]
	}
	if frac == "" {
		if pointZero {
			return whole + ".0"
		}
		return whole
	}
	return whole + "." + frac
}

// Round is Python's round(x, n): to the nearest, halves to even, by the decimal value of x.
func Round(x float64, n int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	s := strconv.FormatFloat(x, 'f', n, 64) // correctly rounded, half to even on the exact binary value
	r, _ := strconv.ParseFloat(s, 64)
	return r
}

// RoundInt is Python's round(x): the nearest integer, halves to even.
func RoundInt(x float64) float64 {
	return math.RoundToEven(x)
}

// Num is a number as the bot shows it: a whole number without a point, else Python's str.
func Num(f float64) string {
	if f == math.Trunc(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e16 {
		return strconv.FormatInt(int64(f), 10)
	}
	return Repr(f)
}

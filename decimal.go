package tricoredb

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// Decimal is an exact decimal number carried as its text, such as "1.50" or
// "-0.000000000000000001".
//
// Bound with [Client.ExecuteParams] or [Client.QueryParams], it travels as a
// JSON string of plain digits, which a DECIMAL column parses exactly. A JSON
// number would be read by the server as a double and lose every digit past a
// double's precision, and an exponent ("1.5E+3") makes the server's tokenizer
// read a DOUBLE, so a Decimal with an exponent is refused before anything is
// sent. Trailing zeros are kept as written.
//
// The server allows a scale of at most 18 and at most 38 significant digits and
// refuses anything beyond that by name; this driver does not repeat that check.
type Decimal string

var decimalPattern = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

// decimalText renders an exact decimal value as plain digits with no exponent.
// isNull is true for a nil *big.Rat or *big.Float.
func decimalText(value any) (text string, isNull bool, err error) {
	switch v := value.(type) {
	case Decimal:
		s := string(v)
		if strings.ContainsAny(s, "eE") {
			return "", false, &ArgumentError{Message: fmt.Sprintf(
				"Decimal %q has an exponent; write it as plain digits, because the server reads an exponent as a DOUBLE", s)}
		}
		if !decimalPattern.MatchString(s) {
			return "", false, &ArgumentError{Message: fmt.Sprintf(
				"Decimal %q is not plain decimal digits (an optional sign, digits, an optional fraction)", s)}
		}
		return s, false, nil
	case *big.Rat:
		if v == nil {
			return "", true, nil
		}
		scale, ok := finiteDecimalScale(v.Denom())
		if !ok {
			return "", false, &ArgumentError{Message: fmt.Sprintf(
				"*big.Rat %s has no finite decimal expansion, so it cannot be sent exactly; round it to a scale first", v.String())}
		}
		return v.FloatString(scale), false, nil
	case *big.Float:
		if v == nil {
			return "", true, nil
		}
		if v.IsInf() {
			return "", false, &ArgumentError{Message: fmt.Sprintf("`%s` has no DECIMAL form", v.Text('g', -1))}
		}
		return v.Text('f', -1), false, nil
	default:
		return "", false, &ArgumentError{Message: fmt.Sprintf("%T is not a decimal type", value)}
	}
}

// finiteDecimalScale reports the number of fractional digits needed to write
// 1/denom exactly, or false when denom has a prime factor other than 2 and 5.
func finiteDecimalScale(denom *big.Int) (int, bool) {
	d := new(big.Int).Set(denom)
	two, five := big.NewInt(2), big.NewInt(5)
	rem := new(big.Int)
	twos, fives := 0, 0
	for {
		q, r := new(big.Int).QuoRem(d, two, rem)
		if r.Sign() != 0 {
			break
		}
		d, twos = q, twos+1
	}
	for {
		q, r := new(big.Int).QuoRem(d, five, rem)
		if r.Sign() != 0 {
			break
		}
		d, fives = q, fives+1
	}
	if d.Cmp(big.NewInt(1)) != 0 {
		return 0, false
	}
	if twos > fives {
		return twos, true
	}
	return fives, true
}

// hexBytes is the 0x-prefixed lowercase hex form a BLOB column parses.
func hexBytes(b []byte) string {
	return "0x" + hex.EncodeToString(b)
}

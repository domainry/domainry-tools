package calculation

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

type CalculationInput struct {
	Operation  string   `json:"operation"`
	Expression string   `json:"expression"`
	Values     []string `json:"values"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
	Precision  *int     `json:"precision"`
	Rounding   string   `json:"rounding"`
	Unit       string   `json:"unit"`
}

var decimalNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)$`)

func ParseDecimal(s string) (*big.Rat, error) {
	if len(s) > 128 || !decimalNumber.MatchString(s) {
		return nil, fmt.Errorf("invalid decimal")
	}
	n, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("invalid decimal")
	}
	return n, nil
}

// A small arithmetic grammar, not a Go/JavaScript interpreter. Token, depth,
// operand and rational sizes are bounded independently of HTTP input limits.
type DecimalParser struct {
	text              string
	at, tokens, depth int
}

func (p *DecimalParser) space() {
	for p.at < len(p.text) && strings.ContainsRune(" \t\r\n", rune(p.text[p.at])) {
		p.at++
	}
}
func (p *DecimalParser) take(c byte) bool {
	p.space()
	if p.at < len(p.text) && p.text[p.at] == c {
		p.at++
		p.tokens++
		return true
	}
	return false
}
func (p *DecimalParser) factor() (*big.Rat, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 32 || p.tokens > 256 {
		return nil, fmt.Errorf("expression limit")
	}
	if p.take('+') {
		return p.factor()
	}
	if p.take('-') {
		v, e := p.factor()
		if e == nil {
			v.Neg(v)
		}
		return v, e
	}
	var value *big.Rat
	var err error
	if p.take('(') {
		value, err = p.sum()
		if err != nil || !p.take(')') {
			return nil, fmt.Errorf("invalid parentheses")
		}
	} else {
		p.space()
		start := p.at
		for p.at < len(p.text) && ((p.text[p.at] >= '0' && p.text[p.at] <= '9') || p.text[p.at] == '.') {
			p.at++
		}
		p.tokens++
		value, err = ParseDecimal(p.text[start:p.at])
		if err != nil {
			return nil, err
		}
	}
	if p.take('%') {
		value.Quo(value, big.NewRat(100, 1))
	}
	return value, nil
}
func BoundedRat(v *big.Rat) error {
	if v.Num().BitLen() > 4096 || v.Denom().BitLen() > 4096 {
		return fmt.Errorf("number limit")
	}
	return nil
}
func (p *DecimalParser) product() (*big.Rat, error) {
	value, err := p.factor()
	if err != nil {
		return nil, err
	}
	for {
		op := byte(0)
		if p.take('*') {
			op = '*'
		} else if p.take('/') {
			op = '/'
		} else {
			return value, nil
		}
		right, err := p.factor()
		if err != nil {
			return nil, err
		}
		if op == '*' {
			value.Mul(value, right)
		} else {
			if right.Sign() == 0 {
				return nil, fmt.Errorf("division by zero")
			}
			value.Quo(value, right)
		}
		if err = BoundedRat(value); err != nil {
			return nil, err
		}
	}
}
func (p *DecimalParser) sum() (*big.Rat, error) {
	value, err := p.product()
	if err != nil {
		return nil, err
	}
	for {
		op := byte(0)
		if p.take('+') {
			op = '+'
		} else if p.take('-') {
			op = '-'
		} else {
			return value, nil
		}
		right, err := p.product()
		if err != nil {
			return nil, err
		}
		if op == '+' {
			value.Add(value, right)
		} else {
			value.Sub(value, right)
		}
		if err = BoundedRat(value); err != nil {
			return nil, err
		}
	}
}

func DecimalRound(value *big.Rat, precision int, rounding string) (string, bool) {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(precision)), nil)
	numerator := new(big.Int).Mul(new(big.Int).Abs(value.Num()), scale)
	integer, remainder := new(big.Int), new(big.Int)
	integer.QuoRem(numerator, value.Denom(), remainder)
	cmp := new(big.Int).Lsh(new(big.Int).Set(remainder), 1).Cmp(value.Denom())
	if rounding != "toward_zero" && (cmp > 0 || cmp == 0 && (rounding == "half_up" || integer.Bit(0) == 1)) {
		integer.Add(integer, big.NewInt(1))
	}
	text := integer.String()
	if precision > 0 {
		if len(text) <= precision {
			text = strings.Repeat("0", precision-len(text)+1) + text
		}
		text = text[:len(text)-precision] + "." + text[len(text)-precision:]
	}
	if value.Sign() < 0 && integer.Sign() != 0 {
		text = "-" + text
	}
	return text, remainder.Sign() != 0
}

func CalculateConversation(in CalculationInput) (map[string]any, error) {
	precision := 2
	if in.Precision != nil {
		precision = *in.Precision
	}
	rounding := in.Rounding
	if rounding == "" {
		rounding = "half_even"
	}
	if precision < 0 || precision > 12 || rounding != "half_even" && rounding != "half_up" && rounding != "toward_zero" || len(in.Unit) > 64 {
		return nil, fmt.Errorf("invalid precision or rounding")
	}
	var value *big.Rat
	var err error
	basis := map[string]any{"operation": in.Operation}
	unit := in.Unit
	switch in.Operation {
	case "expression":
		if len(in.Expression) == 0 || len(in.Expression) > 2048 || len(in.Values) > 0 || in.Start != "" || in.End != "" {
			return nil, fmt.Errorf("invalid expression input")
		}
		p := DecimalParser{text: in.Expression}
		value, err = p.sum()
		p.space()
		if err != nil || p.at != len(p.text) {
			return nil, fmt.Errorf("invalid arithmetic expression")
		}
		basis["expression"] = in.Expression
	case "sum", "mean", "min", "max":
		if len(in.Values) < 1 || len(in.Values) > 256 || in.Expression != "" || in.Start != "" || in.End != "" {
			return nil, fmt.Errorf("invalid statistics input")
		}
		value = new(big.Rat)
		for i, text := range in.Values {
			n, e := ParseDecimal(text)
			if e != nil {
				return nil, e
			}
			switch in.Operation {
			case "sum", "mean":
				value.Add(value, n)
			case "min":
				if i == 0 || value.Cmp(n) > 0 {
					value.Set(n)
				}
			case "max":
				if i == 0 || value.Cmp(n) < 0 {
					value.Set(n)
				}
			}
			if err = BoundedRat(value); err != nil {
				return nil, err
			}
		}
		if in.Operation == "mean" {
			value.Quo(value, big.NewRat(int64(len(in.Values)), 1))
		}
		basis["values"] = in.Values
		basis["count"] = len(in.Values)
	case "date_interval":
		if in.Expression != "" || len(in.Values) > 0 {
			return nil, fmt.Errorf("invalid date input")
		}
		start, e1 := time.Parse("2006-01-02", in.Start)
		end, e2 := time.Parse("2006-01-02", in.End)
		if e1 == nil && e2 == nil {
			unit = "calendar_days"
			value = big.NewRat(end.Unix()-start.Unix(), 86400)
		} else {
			start, e1 = time.Parse(time.RFC3339Nano, in.Start)
			end, e2 = time.Parse(time.RFC3339Nano, in.End)
			if e1 != nil || e2 != nil {
				return nil, fmt.Errorf("use two ISO dates or two RFC3339 instants")
			}
			unit = "elapsed_seconds"
			value = big.NewRat(end.Unix()-start.Unix(), 1)
			value.Add(value, big.NewRat(int64(end.Nanosecond()-start.Nanosecond()), 1e9))
		}
		if in.Unit != "" && in.Unit != unit {
			return nil, fmt.Errorf("date interval unit mismatch")
		}
		basis["start"] = in.Start
		basis["end"] = in.End
	default:
		return nil, fmt.Errorf("unsupported operation")
	}
	if err = BoundedRat(value); err != nil {
		return nil, err
	}
	decimal, rounded := DecimalRound(value, precision, rounding)
	return map[string]any{"value": decimal, "unit": unit, "precision": precision, "rounding": rounding, "rounded": rounded, "exact_numerator": value.Num().String(), "exact_denominator": value.Denom().String(), "basis": basis}, nil
}

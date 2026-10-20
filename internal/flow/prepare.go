package flow

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The prepare step (R11.7): named processors on columns, each compiled to
// Trino SQL, applied in order; a processor changes its column in place, or
// makes a new one when it says "as". The common forty: text, nulls, numbers,
// dates and flags. What a processor can't say is the formula step's.

// A Processor is one of a prepare step's: what to do to which column, with
// its arguments in the order the processor's definition names them.
type Processor struct {
	Column string   `json:"column" yaml:"column"`
	Do     string   `json:"do" yaml:"do"`
	Args   []string `json:"args,omitempty" yaml:"args,omitempty"`
	As     string   `json:"as,omitempty" yaml:"as,omitempty"` // a new column, instead of in place
}

// An Arg is one of a processor's arguments, as the canvas asks for it.
type Arg struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"` // text, number, pattern, format, unit, list, pairs
	Optional bool   `json:"optional,omitempty"`
}

// A ProcessorDef is one processor as the canvas lists it and the compiler
// renders it.
type ProcessorDef struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Group string `json:"group"`
	Args  []Arg  `json:"args"`
	Type  string `json:"type,omitempty"` // the column's type after: "" keeps it, "text" makes it varchar, else as written
	sql   func(x string, a []string, in Column) string
}

var units = []string{"year", "quarter", "month", "week", "day", "hour"}

func lit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// value is an argument as a value of the column's type: a number, a boolean,
// a date or timestamp, else text.
func value(v, typ string) string {
	switch base(typ) {
	case "date":
		return "date " + lit(v)
	case "timestamp":
		return "timestamp " + lit(v)
	}
	return literal(v, typ)
}

// txt is the column as text: itself when it is, cast otherwise.
func txt(x string, in Column) string {
	if in.Type == "" || base(in.Type) == "varchar" || base(in.Type) == "char" {
		return x
	}
	return "cast(" + x + " as varchar)"
}

var processors = []ProcessorDef{
	// Text.
	{Name: "trim", Label: "Trim spaces", Group: "text", Type: "text", sql: func(x string, a []string, in Column) string { return "trim(" + txt(x, in) + ")" }},
	{Name: "upper", Label: "Upper case", Group: "text", Type: "text", sql: func(x string, a []string, in Column) string { return "upper(" + txt(x, in) + ")" }},
	{Name: "lower", Label: "Lower case", Group: "text", Type: "text", sql: func(x string, a []string, in Column) string { return "lower(" + txt(x, in) + ")" }},
	{Name: "title", Label: "Title Case", Group: "text", Type: "text",
		sql: func(x string, a []string, in Column) string {
			return `regexp_replace(lower(` + txt(x, in) + `), '\b(\p{L})', c -> upper(c[1]))`
		}},
	{Name: "squeeze", Label: "Squeeze spaces (one between words, none around)", Group: "text", Type: "text",
		sql: func(x string, a []string, in Column) string {
			return `regexp_replace(trim(` + txt(x, in) + `), '\s+', ' ')`
		}},
	{Name: "left", Label: "Keep the first characters", Group: "text", Type: "text", Args: []Arg{{Name: "how many", Kind: "number"}},
		sql: func(x string, a []string, in Column) string { return "substr(" + txt(x, in) + ", 1, " + a[0] + ")" }},
	{Name: "right", Label: "Keep the last characters", Group: "text", Type: "text", Args: []Arg{{Name: "how many", Kind: "number"}},
		sql: func(x string, a []string, in Column) string { return "substr(" + txt(x, in) + ", -" + a[0] + ")" }},
	{Name: "substring", Label: "Substring", Group: "text", Type: "text", Args: []Arg{{Name: "from (1 is the first)", Kind: "number"}, {Name: "length", Kind: "number", Optional: true}},
		sql: func(x string, a []string, in Column) string {
			if len(a) > 1 && a[1] != "" {
				return "substr(" + txt(x, in) + ", " + a[0] + ", " + a[1] + ")"
			}
			return "substr(" + txt(x, in) + ", " + a[0] + ")"
		}},
	{Name: "replace", Label: "Replace text", Group: "text", Type: "text", Args: []Arg{{Name: "find", Kind: "text"}, {Name: "replace with", Kind: "text", Optional: true}},
		sql: func(x string, a []string, in Column) string {
			return "replace(" + txt(x, in) + ", " + lit(a[0]) + ", " + lit(at(a, 1)) + ")"
		}},
	{Name: "regex_replace", Label: "Replace by pattern", Group: "text", Type: "text", Args: []Arg{{Name: "pattern", Kind: "pattern"}, {Name: "replace with", Kind: "text", Optional: true}},
		sql: func(x string, a []string, in Column) string {
			return "regexp_replace(" + txt(x, in) + ", " + lit(a[0]) + ", " + lit(at(a, 1)) + ")"
		}},
	{Name: "extract", Label: "Extract by pattern", Group: "text", Type: "text", Args: []Arg{{Name: "pattern", Kind: "pattern"}, {Name: "group (0 is the whole match)", Kind: "number", Optional: true}},
		sql: func(x string, a []string, in Column) string {
			if g := at(a, 1); g != "" {
				return "regexp_extract(" + txt(x, in) + ", " + lit(a[0]) + ", " + g + ")"
			}
			return "regexp_extract(" + txt(x, in) + ", " + lit(a[0]) + ")"
		}},
	{Name: "split", Label: "Split, and keep a part", Group: "text", Type: "text", Args: []Arg{{Name: "separator", Kind: "text"}, {Name: "part (1 is the first)", Kind: "number"}},
		sql: func(x string, a []string, in Column) string {
			return "split_part(" + txt(x, in) + ", " + lit(a[0]) + ", " + a[1] + ")"
		}},
	{Name: "prefix", Label: "Add a prefix", Group: "text", Type: "text", Args: []Arg{{Name: "prefix", Kind: "text"}},
		sql: func(x string, a []string, in Column) string { return "concat(" + lit(a[0]) + ", " + txt(x, in) + ")" }},
	{Name: "suffix", Label: "Add a suffix", Group: "text", Type: "text", Args: []Arg{{Name: "suffix", Kind: "text"}},
		sql: func(x string, a []string, in Column) string { return "concat(" + txt(x, in) + ", " + lit(a[0]) + ")" }},
	{Name: "pad_left", Label: "Pad on the left", Group: "text", Type: "text", Args: []Arg{{Name: "to length", Kind: "number"}, {Name: "with", Kind: "text"}},
		sql: func(x string, a []string, in Column) string {
			return "lpad(" + txt(x, in) + ", " + a[0] + ", " + lit(a[1]) + ")"
		}},
	{Name: "pad_right", Label: "Pad on the right", Group: "text", Type: "text", Args: []Arg{{Name: "to length", Kind: "number"}, {Name: "with", Kind: "text"}},
		sql: func(x string, a []string, in Column) string {
			return "rpad(" + txt(x, in) + ", " + a[0] + ", " + lit(a[1]) + ")"
		}},
	{Name: "length", Label: "Length", Group: "text", Type: "bigint", sql: func(x string, a []string, in Column) string { return "length(" + txt(x, in) + ")" }},
	{Name: "hash", Label: "Hash (SHA-256)", Group: "text", Type: "text",
		sql: func(x string, a []string, in Column) string { return "to_hex(sha256(to_utf8(" + txt(x, in) + ")))" }},
	// Nulls.
	{Name: "fill", Label: "Fill nulls with a value", Group: "nulls", Args: []Arg{{Name: "value", Kind: "text"}},
		sql: func(x string, a []string, in Column) string {
			return "coalesce(" + x + ", " + value(a[0], in.Type) + ")"
		}},
	{Name: "null_if", Label: "Null when it equals a value", Group: "nulls", Args: []Arg{{Name: "value", Kind: "text"}},
		sql: func(x string, a []string, in Column) string {
			return "nullif(" + x + ", " + value(a[0], in.Type) + ")"
		}},
	{Name: "empty_to_null", Label: "Null when empty or blank", Group: "nulls", Type: "text",
		sql: func(x string, a []string, in Column) string { return "nullif(trim(" + txt(x, in) + "), '')" }},
	{Name: "null_unless", Label: "Null unless it matches a pattern", Group: "nulls", Type: "text", Args: []Arg{{Name: "pattern", Kind: "pattern"}},
		sql: func(x string, a []string, in Column) string {
			return "case when regexp_like(" + txt(x, in) + ", " + lit(a[0]) + ") then " + txt(x, in) + " end"
		}},
	// Numbers.
	{Name: "parse_number", Label: "Parse as a number (digits, sign and point kept)", Group: "numbers", Type: "double",
		sql: func(x string, a []string, in Column) string {
			return `try_cast(regexp_replace(` + txt(x, in) + `, '[^0-9.eE+-]', '') as double)`
		}},
	{Name: "round", Label: "Round", Group: "numbers", Args: []Arg{{Name: "decimals", Kind: "number"}},
		sql: func(x string, a []string, in Column) string { return "round(" + x + ", " + a[0] + ")" }},
	{Name: "abs", Label: "Absolute value", Group: "numbers", sql: func(x string, a []string, in Column) string { return "abs(" + x + ")" }},
	{Name: "floor", Label: "Round down", Group: "numbers", sql: func(x string, a []string, in Column) string { return "floor(" + x + ")" }},
	{Name: "ceil", Label: "Round up", Group: "numbers", sql: func(x string, a []string, in Column) string { return "ceiling(" + x + ")" }},
	{Name: "clamp", Label: "Clamp between two values", Group: "numbers", Args: []Arg{{Name: "lowest", Kind: "number"}, {Name: "highest", Kind: "number"}},
		sql: func(x string, a []string, in Column) string {
			return "greatest(least(" + x + ", " + a[1] + "), " + a[0] + ")"
		}},
	{Name: "bucket", Label: "Bucket (1 to n, between two values)", Group: "numbers", Type: "bigint", Args: []Arg{{Name: "lowest", Kind: "number"}, {Name: "highest", Kind: "number"}, {Name: "buckets", Kind: "number"}},
		sql: func(x string, a []string, in Column) string {
			return "width_bucket(" + x + ", " + a[0] + ", " + a[1] + ", " + a[2] + ")"
		}},
	{Name: "scale", Label: "Multiply by", Group: "numbers", Args: []Arg{{Name: "factor", Kind: "number"}},
		sql: func(x string, a []string, in Column) string { return x + " * " + a[0] }},
	{Name: "to_text", Label: "To text", Group: "numbers", Type: "text", sql: func(x string, a []string, in Column) string { return "cast(" + x + " as varchar)" }},
	// Dates.
	{Name: "parse_date", Label: "Parse as a date", Group: "dates", Type: "date", Args: []Arg{{Name: "format (%Y-%m-%d; empty for ISO)", Kind: "format", Optional: true}},
		sql: func(x string, a []string, in Column) string {
			if at(a, 0) == "" {
				return "try_cast(" + txt(x, in) + " as date)"
			}
			return "try(date(date_parse(" + txt(x, in) + ", " + lit(a[0]) + ")))"
		}},
	{Name: "parse_timestamp", Label: "Parse as a timestamp", Group: "dates", Type: "timestamp(3)", Args: []Arg{{Name: "format (%Y-%m-%d %H:%i:%s; empty for ISO)", Kind: "format", Optional: true}},
		sql: func(x string, a []string, in Column) string {
			if at(a, 0) == "" {
				return "try_cast(" + txt(x, in) + " as timestamp(3))"
			}
			return "try(date_parse(" + txt(x, in) + ", " + lit(a[0]) + "))"
		}},
	{Name: "format_date", Label: "Format a date as text", Group: "dates", Type: "text", Args: []Arg{{Name: "format (%Y-%m-%d)", Kind: "format"}},
		sql: func(x string, a []string, in Column) string { return "date_format(" + x + ", " + lit(a[0]) + ")" }},
	{Name: "date_part", Label: "A part of a date", Group: "dates", Type: "bigint", Args: []Arg{{Name: "part", Kind: "unit"}},
		sql: func(x string, a []string, in Column) string { return a[0] + "(" + x + ")" }},
	{Name: "truncate_date", Label: "Truncate a date", Group: "dates", Args: []Arg{{Name: "to", Kind: "unit"}},
		sql: func(x string, a []string, in Column) string { return "date_trunc(" + lit(a[0]) + ", " + x + ")" }},
	{Name: "add_days", Label: "Add days (negative to subtract)", Group: "dates", Args: []Arg{{Name: "days", Kind: "number"}},
		sql: func(x string, a []string, in Column) string { return "date_add('day', " + a[0] + ", " + x + ")" }},
	{Name: "days_since", Label: "Days since (to today)", Group: "dates", Type: "bigint",
		sql: func(x string, a []string, in Column) string {
			return "date_diff('day', cast(" + x + " as date), current_date)"
		}},
	// Flags.
	{Name: "to_boolean", Label: "To true or false (yes, y, 1, true; no, n, 0, false)", Group: "flags", Type: "boolean",
		sql: func(x string, a []string, in Column) string {
			t := "lower(trim(" + txt(x, in) + "))"
			return "case when " + t + " in ('true', 't', 'yes', 'y', '1') then true when " + t + " in ('false', 'f', 'no', 'n', '0') then false end"
		}},
	{Name: "not_null_flag", Label: "True when set", Group: "flags", Type: "boolean", sql: func(x string, a []string, in Column) string { return "(" + x + " is not null)" }},
	{Name: "is_in", Label: "True when one of", Group: "flags", Type: "boolean", Args: []Arg{{Name: "values, comma-separated", Kind: "list"}},
		sql: func(x string, a []string, in Column) string {
			var vs []string
			for _, v := range splitList(a[0]) {
				vs = append(vs, value(v, in.Type))
			}
			return "(" + x + " in (" + strings.Join(vs, ", ") + "))"
		}},
	{Name: "standardize", Label: "Standardize values (from=to, comma-separated)", Group: "flags", Args: []Arg{{Name: "from=to, comma-separated", Kind: "pairs"}},
		sql: func(x string, a []string, in Column) string {
			var whens []string
			for _, p := range splitList(a[0]) {
				from, to, _ := strings.Cut(p, "=")
				whens = append(whens, "when "+x+" = "+value(strings.TrimSpace(from), in.Type)+" then "+value(strings.TrimSpace(to), in.Type))
			}
			return "case " + strings.Join(whens, " ") + " else " + x + " end"
		}},
}

func at(a []string, i int) string {
	if i < len(a) {
		return a[i]
	}
	return ""
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Processors are the processors the canvas offers, in order.
func Processors() []ProcessorDef { return processors }

func processor(name string) (ProcessorDef, bool) {
	i := slices.IndexFunc(processors, func(d ProcessorDef) bool { return d.Name == name })
	if i < 0 {
		return ProcessorDef{}, false
	}
	return processors[i], true
}

// checkArgs refuses an argument a processor can't take: a number that isn't
// one, a unit that isn't a part of a date, a pair without its "=", anything
// that could be more than a value.
func checkArgs(d ProcessorDef, args []string) error {
	for i, arg := range d.Args {
		v := strings.TrimSpace(at(args, i))
		if v == "" {
			if arg.Optional {
				continue
			}
			return fmt.Errorf("%s needs %s", d.Label, arg.Name)
		}
		switch arg.Kind {
		case "number":
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return fmt.Errorf("%s: %s is a number, not %q", d.Label, arg.Name, v)
			}
		case "unit":
			if !slices.Contains(units, v) {
				return fmt.Errorf("%s: %s is one of %s, not %q", d.Label, arg.Name, strings.Join(units, ", "), v)
			}
		case "pairs":
			for _, p := range splitList(v) {
				if !strings.Contains(p, "=") {
					return fmt.Errorf("%s: each pair is from=to; %q isn't", d.Label, p)
				}
			}
		case "list":
			if len(splitList(v)) == 0 {
				return fmt.Errorf("%s needs %s", d.Label, arg.Name)
			}
		}
	}
	if len(args) > len(d.Args) {
		return fmt.Errorf("%s takes %d argument(s), not %d", d.Label, len(d.Args), len(args))
	}
	return nil
}

// compilePrepare renders a prepare step: each processor in order, on the
// column as the ones before left it, in one select.
func compilePrepare(s *Step, in []Column, from string) ([]Column, string, error) {
	if len(s.Processors) == 0 {
		return nil, "", fmt.Errorf("a prepare step applies at least one processor")
	}
	out := slices.Clone(in)
	exprs := map[string]string{} // a column's expression so far, when a processor touched it
	for i, p := range s.Processors {
		d, ok := processor(p.Do)
		if !ok {
			return nil, "", fmt.Errorf("processor %d: no processor %q", i+1, p.Do)
		}
		j := slices.IndexFunc(out, func(c Column) bool { return c.Name == p.Column })
		if j < 0 {
			return nil, "", fmt.Errorf("%s: no column %s in the step's input", d.Label, p.Column)
		}
		args := make([]string, len(p.Args))
		for k, a := range p.Args {
			args[k] = strings.TrimSpace(a)
		}
		if err := checkArgs(d, args); err != nil {
			return nil, "", err
		}
		col := out[j]
		x := exprs[col.Name]
		if x == "" {
			x = q(col.Name)
		}
		typ := col.Type
		switch d.Type {
		case "":
		case "text":
			typ = "varchar"
		default:
			typ = d.Type
		}
		expr := d.sql(x, args, col)
		target := col.Name
		if p.As != "" {
			if !NameOK(p.As) {
				return nil, "", fmt.Errorf("a column's name is lowercase letters, digits and underscores: %q isn't", p.As)
			}
			target = p.As
		}
		exprs[target] = expr
		if k := slices.IndexFunc(out, func(c Column) bool { return c.Name == target }); k >= 0 {
			out[k].Type = typ
		} else {
			out = append(out, Column{Name: target, Type: typ})
		}
	}
	var parts []string
	for _, c := range out {
		if e, ok := exprs[c.Name]; ok {
			parts = append(parts, e+" as "+q(c.Name))
		} else {
			parts = append(parts, q(c.Name))
		}
	}
	return out, "select " + strings.Join(parts, ", ") + "\nfrom " + from, nil
}

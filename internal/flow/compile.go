package flow

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Env is what the compiler knows of the project: where its tables are, and
// their columns (the catalog's, with their types), so a step's columns are
// worked out before anything runs. CodeColumns are code steps' columns
// when Trino has said what they return (a step not there has none known).
type Env struct {
	Catalog     string
	Namespace   string
	Columns     func(catalog, namespace, table string) ([]Column, error)
	CodeColumns map[string][]Column
	// MetricSQL is the semantic layer's SQL for a metric query, and its
	// columns; an empty SQL with no error means it can't be told yet (no one
	// to ask as).
	MetricSQL func(metrics, groupBy []string, where string) (string, []Column, error)
}

// A StepResult is what the compiler made of a step: its columns, the SQL
// of its model (dbt's refs and sources in it), or why it couldn't.
type StepResult struct {
	Model   string   `json:"model"`
	Columns []Column `json:"columns"`
	SQL     string   `json:"sql"`
	Error   string   `json:"error,omitempty"`
	// Unknown says the columns can't be told without running it: a code
	// step Trino hasn't described yet.
	Unknown bool `json:"unknown,omitempty"`
}

// Compiled is a flow with each step's result.
type Compiled struct {
	Flow  *Flow
	Order []string // the steps, each after its inputs
	Steps map[string]*StepResult
}

// Errors are the steps that failed, in order.
func (c *Compiled) Errors() []string {
	var out []string
	for _, id := range c.Order {
		if c.Steps[id].Error != "" {
			out = append(out, id+": "+c.Steps[id].Error)
		}
	}
	return out
}

// Compile checks the flow's shape, then each step in order: its columns and
// types from its inputs', and its SQL. A step that can't be compiled has an
// error and no columns; the steps after it say which input failed.
func Compile(f *Flow, env Env) (*Compiled, error) {
	if err := f.Check(); err != nil {
		return nil, err
	}
	if env.Catalog == "" {
		env.Catalog = "iceberg"
	}
	for i := range f.Steps {
		s := &f.Steps[i]
		if s.Kind == Input {
			if s.Catalog == "" {
				s.Catalog = env.Catalog
			}
			if s.Namespace == "" {
				s.Namespace = env.Namespace
			}
		}
	}
	order, _ := f.order()
	c := &Compiled{Flow: f, Order: order, Steps: map[string]*StepResult{}}
	for _, id := range order {
		s := f.Step(id)
		res := &StepResult{Model: ModelName(f.Name, s)}
		c.Steps[id] = res
		var inputs [][]Column
		for _, in := range s.Inputs {
			r := c.Steps[in]
			if r.Error != "" {
				res.Error = "its input " + in + " has an error"
				break
			}
			if r.Unknown {
				res.Unknown = true // its columns can't be told until Trino describes the code step before it
			}
			inputs = append(inputs, r.Columns)
		}
		if res.Error != "" || res.Unknown && s.Kind != Code {
			continue
		}
		if s.Kind == Code {
			res.SQL = s.SQL
			if cols, ok := env.CodeColumns[id]; ok {
				res.Columns = cols
			} else {
				res.Unknown = true
			}
			continue
		}
		if s.Kind == Metric {
			switch {
			case len(s.Metrics) == 0:
				res.Error = "a metric step names at least one metric"
			case env.MetricSQL == nil:
				res.Error = "the semantic layer isn't set up, which gives a metric step its SQL"
			default:
				sql, cols, err := env.MetricSQL(s.Metrics, s.By, s.Where)
				switch {
				case err != nil:
					res.Error = err.Error()
				case sql == "":
					res.Unknown = true
				default:
					res.SQL, res.Columns = strings.TrimSpace(sql), cols
				}
			}
			continue
		}
		cols, sql, err := compileStep(s, inputs, env, dbtRefs(f))
		if err != nil {
			res.Error = err.Error()
			continue
		}
		res.Columns, res.SQL = cols, sql
	}
	return c, nil
}

// refs say how a step's SQL names its inputs and its tables: dbt's way in
// the models, CTEs and sampled tables in a preview.
type refs struct {
	step  func(id string) string
	table func(s *Step) string
}

func dbtRefs(f *Flow) refs {
	return refs{
		step: func(id string) string { return "{{ ref('" + ModelName(f.Name, f.Step(id)) + "') }}" },
		table: func(s *Step) string {
			return "{{ source('" + SourceName(f.Name, s.Catalog, s.Namespace) + "', '" + s.Table + "') }}"
		},
	}
}

func q(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func names(cols []Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

func find(cols []Column, name string) (Column, bool) {
	for _, c := range cols {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

func need(cols []Column, name, what string) (Column, error) {
	if name == "" {
		return Column{}, fmt.Errorf("%s names no column", what)
	}
	c, ok := find(cols, name)
	if !ok {
		return Column{}, fmt.Errorf("%s: no column %s in its input (it has %s)", what, name, strings.Join(names(cols), ", "))
	}
	return c, nil
}

func compileStep(s *Step, inputs [][]Column, env Env, r refs) ([]Column, string, error) {
	var in []Column
	if len(inputs) > 0 {
		in = inputs[0]
	}
	from := func(i int) string { return r.step(s.Inputs[i]) }
	switch s.Kind {
	case Input:
		if !NameOK(s.Table) || !NameOK(s.Namespace) || !NameOK(s.Catalog) {
			return nil, "", fmt.Errorf("an input names a table (lowercase letters, digits and underscores): %q isn't one", s.Table)
		}
		if env.Columns == nil {
			return nil, "", fmt.Errorf("the catalog isn't set up, so the table's columns can't be read")
		}
		cols, err := env.Columns(s.Catalog, s.Namespace, s.Table)
		if err != nil {
			return nil, "", err
		}
		return cols, "select * from " + r.table(s), nil

	case Select:
		if len(s.Columns) == 0 {
			return nil, "", fmt.Errorf("a select step keeps at least one column")
		}
		var out []Column
		var parts []string
		for _, cs := range s.Columns {
			c, err := need(in, cs.Name, "select")
			if err != nil {
				return nil, "", err
			}
			as := cs.As
			if as == "" {
				as = cs.Name
			}
			if !NameOK(as) {
				return nil, "", fmt.Errorf("a column's new name is lowercase letters, digits and underscores: %q isn't", as)
			}
			if _, dup := find(out, as); dup {
				return nil, "", fmt.Errorf("two columns named %s", as)
			}
			out = append(out, Column{Name: as, Type: c.Type})
			if as == cs.Name {
				parts = append(parts, q(cs.Name))
			} else {
				parts = append(parts, q(cs.Name)+" as "+q(as))
			}
		}
		return out, "select " + strings.Join(parts, ", ") + "\nfrom " + from(0), nil

	case Retype:
		if len(s.Columns) == 0 {
			return nil, "", fmt.Errorf("a retype step casts at least one column")
		}
		casts := map[string]string{}
		for _, cs := range s.Columns {
			if _, err := need(in, cs.Name, "retype"); err != nil {
				return nil, "", err
			}
			if !typeRE.MatchString(cs.Type) {
				return nil, "", fmt.Errorf("retype %s: %q isn't a Trino type (varchar, bigint, double, decimal(10,2), date, timestamp(6), boolean)", cs.Name, cs.Type)
			}
			casts[cs.Name] = cs.Type
		}
		out := slices.Clone(in)
		var parts []string
		for i, c := range out {
			if t, ok := casts[c.Name]; ok {
				out[i].Type = t
				parts = append(parts, "cast("+q(c.Name)+" as "+t+") as "+q(c.Name))
			} else {
				parts = append(parts, q(c.Name))
			}
		}
		return out, "select " + strings.Join(parts, ", ") + "\nfrom " + from(0), nil

	case Filter:
		if strings.TrimSpace(s.Where) == "" {
			return nil, "", fmt.Errorf("a filter step needs a condition")
		}
		if err := expressionOK(s.Where); err != nil {
			return nil, "", err
		}
		return slices.Clone(in), "select *\nfrom " + from(0) + "\nwhere " + strings.TrimSpace(s.Where), nil

	case Join:
		kind := s.Join
		if kind == "" {
			kind = "inner"
		}
		if !slices.Contains(Joins, kind) {
			return nil, "", fmt.Errorf("a join is inner, left or full, not %q", kind)
		}
		if len(s.On) == 0 {
			return nil, "", fmt.Errorf("a join needs at least one pair of columns to join on")
		}
		left, right := inputs[0], inputs[1]
		var conds []string
		for _, p := range s.On {
			l, err := need(left, p.Left, "join, on the left ("+s.Inputs[0]+")")
			if err != nil {
				return nil, "", err
			}
			rc, err := need(right, p.Right, "join, on the right ("+s.Inputs[1]+")")
			if err != nil {
				return nil, "", err
			}
			if !compatible(l.Type, rc.Type) {
				return nil, "", fmt.Errorf("join on %s.%s (%s) and %s.%s (%s): their types differ; retype one first",
					s.Inputs[0], l.Name, l.Type, s.Inputs[1], rc.Name, rc.Type)
			}
			conds = append(conds, "l."+q(p.Left)+" = r."+q(p.Right))
		}
		var out []Column
		var parts []string
		for _, c := range left {
			out = append(out, c)
			parts = append(parts, "l."+q(c.Name))
		}
		for _, c := range right {
			name := c.Name
			if _, clash := find(left, name); clash {
				name = c.Name + "_right"
				for _, dup := find(out, name); dup; _, dup = find(out, name) {
					name += "_"
				}
			}
			out = append(out, Column{Name: name, Type: c.Type})
			if name == c.Name {
				parts = append(parts, "r."+q(c.Name))
			} else {
				parts = append(parts, "r."+q(c.Name)+" as "+q(name))
			}
		}
		return out, fmt.Sprintf("select %s\nfrom %s as l\n%s join %s as r on %s",
			strings.Join(parts, ", "), from(0), kind, from(1), strings.Join(conds, " and ")), nil

	case Union:
		var queries []string
		for i, cols := range inputs {
			var parts []string
			for _, c := range in {
				other, ok := find(cols, c.Name)
				if !ok {
					return nil, "", fmt.Errorf("union: input %s has no column %s, which %s has", s.Inputs[i], c.Name, s.Inputs[0])
				}
				if !compatible(c.Type, other.Type) {
					return nil, "", fmt.Errorf("union: %s in %s is %s, in %s %s; retype one first", c.Name, s.Inputs[0], c.Type, s.Inputs[i], other.Type)
				}
				parts = append(parts, q(c.Name))
			}
			if len(cols) != len(in) {
				return nil, "", fmt.Errorf("union: input %s has %d columns, %s has %d; select the same columns in each first", s.Inputs[i], len(cols), s.Inputs[0], len(in))
			}
			queries = append(queries, "select "+strings.Join(parts, ", ")+" from "+from(i))
		}
		op := "\nunion all\n"
		if s.Distinct {
			op = "\nunion\n"
		}
		return slices.Clone(in), strings.Join(queries, op), nil

	case Dedupe:
		if len(s.By) == 0 {
			return slices.Clone(in), "select distinct *\nfrom " + from(0), nil
		}
		for _, b := range s.By {
			if _, err := need(in, b, "dedupe by"); err != nil {
				return nil, "", err
			}
		}
		order, err := orderBy(in, s.Order, "dedupe")
		if err != nil {
			return nil, "", err
		}
		if order == "" {
			order = quoted(s.By)
		}
		return slices.Clone(in), fmt.Sprintf("select %s\nfrom (\n  select *, row_number() over (partition by %s order by %s) as storscale_rn\n  from %s\n)\nwhere storscale_rn = 1",
			quoted(names(in)), quoted(s.By), order, from(0)), nil

	case Group:
		var out []Column
		var parts []string
		for _, b := range s.By {
			c, err := need(in, b, "group by")
			if err != nil {
				return nil, "", err
			}
			out = append(out, c)
			parts = append(parts, q(b))
		}
		if len(s.Aggregates) == 0 {
			return nil, "", fmt.Errorf("a group step needs at least one aggregate (sum, count, min, max, avg, count_distinct)")
		}
		for _, a := range s.Aggregates {
			if !slices.Contains(Aggregates, a.Function) {
				return nil, "", fmt.Errorf("group: no aggregate %q (one of %s)", a.Function, strings.Join(Aggregates, ", "))
			}
			as := a.As
			var expr, typ string
			if a.Function == "count" && a.Column == "" {
				expr, typ = "count(*)", "bigint"
				if as == "" {
					as = "count"
				}
			} else {
				c, err := need(in, a.Column, "group, "+a.Function)
				if err != nil {
					return nil, "", err
				}
				if as == "" {
					as = a.Function + "_" + a.Column
				}
				switch a.Function {
				case "count_distinct":
					expr, typ = "count(distinct "+q(a.Column)+")", "bigint"
				case "count":
					expr, typ = "count("+q(a.Column)+")", "bigint"
				case "sum":
					if !numeric(c.Type) {
						return nil, "", fmt.Errorf("group: sum of %s, which is %s, not a number", a.Column, c.Type)
					}
					expr, typ = "sum("+q(a.Column)+")", sumType(c.Type)
				case "avg":
					if !numeric(c.Type) {
						return nil, "", fmt.Errorf("group: avg of %s, which is %s, not a number", a.Column, c.Type)
					}
					expr, typ = "avg("+q(a.Column)+")", avgType(c.Type)
				default:
					expr, typ = a.Function+"("+q(a.Column)+")", c.Type
				}
			}
			if !NameOK(as) {
				return nil, "", fmt.Errorf("a column's name is lowercase letters, digits and underscores: %q isn't", as)
			}
			if _, dup := find(out, as); dup {
				return nil, "", fmt.Errorf("two columns named %s", as)
			}
			out = append(out, Column{Name: as, Type: typ})
			parts = append(parts, expr+" as "+q(as))
		}
		sql := "select " + strings.Join(parts, ", ") + "\nfrom " + from(0)
		if len(s.By) > 0 {
			sql += "\ngroup by " + quoted(s.By)
		}
		return out, sql, nil

	case Pivot:
		col, err := need(in, s.Column, "pivot, the column whose values become columns")
		if err != nil {
			return nil, "", err
		}
		val, err := need(in, s.Value, "pivot, the value")
		if err != nil {
			return nil, "", err
		}
		fn := s.Function
		if fn == "" {
			fn = "sum"
		}
		if !slices.Contains([]string{"sum", "count", "min", "max", "avg"}, fn) {
			return nil, "", fmt.Errorf("pivot: no aggregate %q (sum, count, min, max, avg)", fn)
		}
		if (fn == "sum" || fn == "avg") && !numeric(val.Type) {
			return nil, "", fmt.Errorf("pivot: %s of %s, which is %s, not a number", fn, val.Name, val.Type)
		}
		if len(s.Values) == 0 {
			return nil, "", fmt.Errorf("pivot: say which values of %s become columns (Trino needs them named)", s.Column)
		}
		var out []Column
		var parts []string
		for _, b := range s.By {
			c, err := need(in, b, "pivot by")
			if err != nil {
				return nil, "", err
			}
			out = append(out, c)
			parts = append(parts, q(b))
		}
		typ := map[string]string{"sum": sumType(val.Type), "avg": avgType(val.Type), "count": "bigint"}[fn]
		if typ == "" {
			typ = val.Type
		}
		for _, v := range s.Values {
			name := columnName(v)
			if _, dup := find(out, name); dup {
				return nil, "", fmt.Errorf("pivot: two columns would be named %s", name)
			}
			out = append(out, Column{Name: name, Type: typ})
			parts = append(parts, fmt.Sprintf("%s(case when %s = %s then %s end) as %s", fn, q(col.Name), literal(v, col.Type), q(val.Name), q(name)))
		}
		sql := "select " + strings.Join(parts, ", ") + "\nfrom " + from(0)
		if len(s.By) > 0 {
			sql += "\ngroup by " + quoted(s.By)
		}
		return out, sql, nil

	case Unpivot:
		if len(s.Unpivot) == 0 {
			return nil, "", fmt.Errorf("an unpivot step names the columns that become rows")
		}
		nameCol, valueCol := s.Name, s.Value
		if nameCol == "" {
			nameCol = "name"
		}
		if valueCol == "" {
			valueCol = "value"
		}
		var typ string
		var labels, values []string
		for _, u := range s.Unpivot {
			c, err := need(in, u, "unpivot")
			if err != nil {
				return nil, "", err
			}
			if typ == "" {
				typ = c.Type
			} else if !compatible(typ, c.Type) {
				return nil, "", fmt.Errorf("unpivot: %s is %s and %s is %s; retype them to one type first", s.Unpivot[0], typ, u, c.Type)
			}
			labels = append(labels, "'"+strings.ReplaceAll(u, "'", "''")+"'")
			values = append(values, q(u))
		}
		var out []Column
		var parts []string
		for _, c := range in {
			if slices.Contains(s.Unpivot, c.Name) {
				continue
			}
			out = append(out, c)
			parts = append(parts, q(c.Name))
		}
		for _, n := range []string{nameCol, valueCol} {
			if !NameOK(n) || slices.ContainsFunc(out, func(c Column) bool { return c.Name == n }) {
				return nil, "", fmt.Errorf("unpivot: %q can't name the new column (taken, or not an identifier)", n)
			}
		}
		out = append(out, Column{Name: nameCol, Type: "varchar"}, Column{Name: valueCol, Type: typ})
		parts = append(parts, "t."+q(nameCol), "t."+q(valueCol))
		return out, fmt.Sprintf("select %s\nfrom %s\ncross join unnest(array[%s], array[%s]) as t (%s, %s)",
			strings.Join(parts, ", "), from(0), strings.Join(labels, ", "), strings.Join(values, ", "), q(nameCol), q(valueCol)), nil

	case Window:
		if len(s.Windows) == 0 {
			return nil, "", fmt.Errorf("a window step adds at least one column (rank, running_sum, lag, ...)")
		}
		for _, p := range s.Partition {
			if _, err := need(in, p, "window, partition by"); err != nil {
				return nil, "", err
			}
		}
		order, err := orderBy(in, s.Order, "window")
		if err != nil {
			return nil, "", err
		}
		over := ""
		if len(s.Partition) > 0 {
			over = "partition by " + quoted(s.Partition)
		}
		if order != "" {
			over = strings.TrimSpace(over + " order by " + order)
		}
		out := slices.Clone(in)
		parts := quotedList(names(in))
		for _, w := range s.Windows {
			if !slices.Contains(Windows, w.Function) {
				return nil, "", fmt.Errorf("window: no function %q (one of %s)", w.Function, strings.Join(Windows, ", "))
			}
			if !NameOK(w.As) {
				return nil, "", fmt.Errorf("window: %s needs a name for its column (lowercase letters, digits and underscores)", w.Function)
			}
			if _, dup := find(out, w.As); dup {
				return nil, "", fmt.Errorf("two columns named %s", w.As)
			}
			ordered := slices.Contains([]string{"rank", "dense_rank", "row_number", "running_sum", "lag", "lead"}, w.Function)
			if ordered && order == "" {
				return nil, "", fmt.Errorf("window: %s needs an order", w.Function)
			}
			var expr, typ string
			switch w.Function {
			case "rank", "dense_rank", "row_number":
				expr, typ = w.Function+"() over ("+over+")", "bigint"
			default:
				c, err := need(in, w.Column, "window, "+w.Function)
				if err != nil {
					return nil, "", err
				}
				switch w.Function {
				case "running_sum":
					if !numeric(c.Type) {
						return nil, "", fmt.Errorf("window: a running sum of %s, which is %s, not a number", c.Name, c.Type)
					}
					expr, typ = "sum("+q(c.Name)+") over ("+over+" rows between unbounded preceding and current row)", sumType(c.Type)
				case "sum":
					if !numeric(c.Type) {
						return nil, "", fmt.Errorf("window: a sum of %s, which is %s, not a number", c.Name, c.Type)
					}
					expr, typ = "sum("+q(c.Name)+") over ("+over+")", sumType(c.Type)
				case "avg":
					if !numeric(c.Type) {
						return nil, "", fmt.Errorf("window: an average of %s, which is %s, not a number", c.Name, c.Type)
					}
					expr, typ = "avg("+q(c.Name)+") over ("+over+")", avgType(c.Type)
				case "count":
					expr, typ = "count("+q(c.Name)+") over ("+over+")", "bigint"
				case "lag", "lead":
					n := w.Offset
					if n <= 0 {
						n = 1
					}
					expr, typ = fmt.Sprintf("%s(%s, %d) over (%s)", w.Function, q(c.Name), n, over), c.Type
				default: // min, max
					expr, typ = w.Function+"("+q(c.Name)+") over ("+over+")", c.Type
				}
			}
			out = append(out, Column{Name: w.As, Type: typ})
			parts = append(parts, expr+" as "+q(w.As))
		}
		return out, "select " + strings.Join(parts, ", ") + "\nfrom " + from(0), nil

	case Formula:
		if len(s.Columns) == 0 {
			return nil, "", fmt.Errorf("a formula step computes at least one column")
		}
		out := slices.Clone(in)
		exprs := map[string]string{}
		for _, cs := range s.Columns {
			if !NameOK(cs.Name) {
				return nil, "", fmt.Errorf("a column's name is lowercase letters, digits and underscores: %q isn't", cs.Name)
			}
			if strings.TrimSpace(cs.Expression) == "" {
				return nil, "", fmt.Errorf("formula %s: no expression", cs.Name)
			}
			if err := expressionOK(cs.Expression); err != nil {
				return nil, "", err
			}
			if cs.Type != "" && !typeRE.MatchString(cs.Type) {
				return nil, "", fmt.Errorf("formula %s: %q isn't a Trino type", cs.Name, cs.Type)
			}
			if _, dup := exprs[cs.Name]; dup {
				return nil, "", fmt.Errorf("two columns named %s", cs.Name)
			}
			expr := "(" + strings.TrimSpace(cs.Expression) + ")"
			if cs.Type != "" {
				expr = "cast(" + expr + " as " + cs.Type + ")"
			}
			exprs[cs.Name] = expr
			if i := slices.IndexFunc(out, func(c Column) bool { return c.Name == cs.Name }); i >= 0 {
				out[i].Type = cs.Type
			} else {
				out = append(out, Column{Name: cs.Name, Type: cs.Type})
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
		return out, "select " + strings.Join(parts, ", ") + "\nfrom " + from(0), nil

	case Output:
		if !NameOK(s.Table) {
			return nil, "", fmt.Errorf("the output names the table it makes (lowercase letters, digits and underscores): %q isn't one", s.Table)
		}
		if len(in) == 0 {
			return nil, "", fmt.Errorf("its input has no columns")
		}
		for _, ch := range s.Checks {
			if _, err := need(in, ch.Column, "check "+ch.Test); err != nil {
				return nil, "", err
			}
			if !slices.Contains(Tests, ch.Test) {
				return nil, "", fmt.Errorf("check on %s: no test %q (one of %s)", ch.Column, ch.Test, strings.Join(Tests, ", "))
			}
			if ch.Test == "accepted_values" && len(ch.Values) == 0 {
				return nil, "", fmt.Errorf("check on %s: accepted_values needs the values", ch.Column)
			}
		}
		return slices.Clone(in), "select *\nfrom " + from(0), nil
	}
	return nil, "", fmt.Errorf("no step kind %q", s.Kind)
}

var forbiddenRE = regexp.MustCompile(`(?i)(;|--|/\*|\{\{|\{%)`)

// expressionOK refuses what can't be one expression: a second statement, a
// comment (which could hide the rest of the query), Jinja.
func expressionOK(e string) error {
	if m := forbiddenRE.FindString(e); m != "" {
		return fmt.Errorf("an expression can't contain %q", m)
	}
	return nil
}

func quoted(cols []string) string { return strings.Join(quotedList(cols), ", ") }

func quotedList(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = q(c)
	}
	return out
}

func orderBy(in []Column, order []Ordering, what string) (string, error) {
	var parts []string
	for _, o := range order {
		if _, err := need(in, o.Column, what+", order by"); err != nil {
			return "", err
		}
		p := q(o.Column)
		if o.Desc {
			p += " desc"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", "), nil
}

var notIdentRE = regexp.MustCompile(`[^a-z0-9_]+`)

// columnName is a pivot value as a column: "North America" -> north_america.
func columnName(v string) string {
	n := strings.Trim(notIdentRE.ReplaceAllString(strings.ToLower(v), "_"), "_")
	if n == "" || n[0] >= '0' && n[0] <= '9' {
		n = "v_" + n
	}
	return n
}

// literal is a pivot value in SQL, as the column's type takes it.
func literal(v, typ string) string {
	if numeric(typ) {
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			return v
		}
	}
	if typ == "boolean" && (v == "true" || v == "false") {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

func base(typ string) string {
	if i := strings.IndexByte(typ, '('); i >= 0 {
		return typ[:i]
	}
	return typ
}

func numeric(typ string) bool {
	return typ == "" || slices.Contains([]string{"tinyint", "smallint", "integer", "bigint", "real", "double", "decimal"}, base(typ))
}

// compatible says two types can be compared or stacked: the same, both
// numbers, both strings; an unknown type matches anything.
func compatible(a, b string) bool {
	switch {
	case a == "" || b == "" || a == b:
		return true
	case numeric(a) && numeric(b):
		return true
	case base(a) == "varchar" || base(a) == "char":
		return base(b) == "varchar" || base(b) == "char"
	case base(a) == "timestamp" && base(b) == "timestamp":
		return strings.HasSuffix(a, "with time zone") == strings.HasSuffix(b, "with time zone")
	}
	return false
}

func sumType(typ string) string {
	switch base(typ) {
	case "tinyint", "smallint", "integer", "bigint":
		return "bigint"
	case "decimal":
		if _, s, ok := strings.Cut(typ, ","); ok {
			return "decimal(38," + strings.TrimSpace(strings.TrimSuffix(s, ")")) + ")"
		}
		return "decimal(38,0)"
	}
	return typ
}

func avgType(typ string) string {
	switch base(typ) {
	case "tinyint", "smallint", "integer", "bigint":
		return "double"
	}
	return typ
}

// --- Previews ------------------------------------------------------------------

// Sampler says how much of a table a preview reads: a percentage (100 is
// all of it), so a preview of a large table costs seconds.
type Sampler func(catalog, namespace, table string) float64

// Preview is the query that shows a step's result: the steps up to it as
// CTEs, each input sampled, the first rows of the step. A code step's refs
// and sources are turned into the CTEs and tables; other Jinja it can't run.
func (c *Compiled) Preview(stepID string, sample Sampler, limit int) (string, error) {
	f := c.Flow
	s := f.Step(stepID)
	if s == nil {
		return "", fmt.Errorf("no step %s", stepID)
	}
	for _, id := range c.Order {
		if r := c.Steps[id]; r.Error != "" && (id == stepID || upstream(f, stepID, id)) {
			return "", fmt.Errorf("step %s: %s", id, r.Error)
		}
	}
	r := refs{
		step: func(id string) string { return q(id) },
		table: func(s *Step) string {
			t := q(s.Catalog) + "." + q(s.Namespace) + "." + q(s.Table)
			if p := sample(s.Catalog, s.Namespace, s.Table); p > 0 && p < 100 {
				t += " tablesample bernoulli (" + strconv.FormatFloat(p, 'f', -1, 64) + ")"
			}
			return t
		},
	}
	var ctes []string
	for _, id := range c.Order {
		if id != stepID && !upstream(f, stepID, id) {
			continue
		}
		st := f.Step(id)
		var sql string
		if st.Kind == Code {
			var err error
			if sql, err = codeSQL(f, st, r); err != nil {
				return "", err
			}
		} else if st.Kind == Metric {
			if sql = c.Steps[id].SQL; sql == "" {
				return "", fmt.Errorf("step %s: its SQL isn't known yet (sign in, so the semantic layer can be asked as you)", id)
			}
		} else {
			var inputs [][]Column
			for _, in := range st.Inputs {
				inputs = append(inputs, c.Steps[in].Columns)
			}
			_, sql, _ = compileStep(st, inputs, Env{Columns: func(_, _, _ string) ([]Column, error) { return nil, nil }}, r)
		}
		ctes = append(ctes, q(id)+" as (\n"+indent(sql)+"\n)")
	}
	return fmt.Sprintf("with %s\nselect *\nfrom %s\nlimit %d", strings.Join(ctes, ",\n"), q(stepID), limit), nil
}

// upstream says whether step from feeds step to, directly or not.
func upstream(f *Flow, to, from string) bool {
	s := f.Step(to)
	for _, in := range s.Inputs {
		if in == from || upstream(f, in, from) {
			return true
		}
	}
	return false
}

// codeSQL is a code step's model as a preview can run it: its config gone,
// its refs the CTEs, its sources the tables.
func codeSQL(f *Flow, s *Step, r refs) (string, error) {
	sql := configRE.ReplaceAllString(s.SQL, "")
	byModel := map[string]string{}
	for i := range f.Steps {
		byModel[ModelName(f.Name, &f.Steps[i])] = f.Steps[i].ID
	}
	sql = refRE.ReplaceAllStringFunc(sql, func(m string) string {
		return r.step(byModel[refRE.FindStringSubmatch(m)[1]])
	})
	var err error
	sql = sourceRE.ReplaceAllStringFunc(sql, func(m string) string {
		sm := sourceRE.FindStringSubmatch(m)
		for _, src := range f.Sources() {
			if src.Name == sm[1] {
				return r.table(&Step{Catalog: src.Catalog, Namespace: src.Namespace, Table: sm[2]})
			}
		}
		err = fmt.Errorf("step %s reads source %s, which the flow's sources.yml doesn't have", s.ID, sm[1])
		return m
	})
	if err != nil {
		return "", err
	}
	if strings.Contains(sql, "{{") || strings.Contains(sql, "{%") {
		return "", fmt.Errorf("step %s uses Jinja the canvas can't run (only ref, source and config); a preview can't show it", s.ID)
	}
	return strings.TrimSpace(sql), nil
}

func indent(sql string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(sql), "\n", "\n  ")
}

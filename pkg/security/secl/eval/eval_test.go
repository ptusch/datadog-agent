package eval

import (
	"fmt"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/pkg/errors"

	"github.com/DataDog/datadog-agent/pkg/security/policy"
	"github.com/DataDog/datadog-agent/pkg/security/secl/ast"
)

func generateMacroEvaluators(t *testing.T, model Model, opts *Opts, macros map[policy.MacroID]*ast.Macro) {
	opts.Macros = make(map[policy.MacroID]*MacroEvaluator)
	for name, macro := range macros {
		eval, err := MacroToEvaluator(macro, model, opts, "")
		if err != nil {
			t.Fatal(err)
		}
		opts.Macros[name] = eval
	}
}

func parse(t *testing.T, expr string, model Model, opts *Opts, macros map[string]*ast.Macro) (*RuleEvaluator, *ast.Rule, error) {
	rule, err := ast.ParseRule(expr)
	if err != nil {
		t.Fatal(fmt.Sprintf("%s\n%s", err, expr))
	}

	generateMacroEvaluators(t, model, opts, macros)

	evaluator, err := RuleToEvaluator(rule, model, opts)
	if err != nil {
		return nil, rule, err
	}

	return evaluator, rule, err
}

func generatePartials(t *testing.T, field Field, model Model, opts *Opts, evaluator *RuleEvaluator, rule *ast.Rule, macros map[policy.MacroID]*ast.Macro) {
	macroPartials := make(map[policy.MacroID]*MacroEvaluator)

	for id, macro := range macros {
		eval, err := MacroToEvaluator(macro, model, opts, field)
		if err != nil {
			t.Fatal(err)
		}
		macroPartials[id] = eval
	}

	state := newState(model, field, macroPartials)
	pEval, _, _, err := nodeToEvaluator(rule.BooleanExpression, opts, state)
	if err != nil {
		t.Fatal(errors.Wrapf(err, "couldn't generate partial for field %s and rule %s", field, rule.Expr))
	}
	pEvalBool, ok := pEval.(*BoolEvaluator)
	if !ok {
		t.Fatal("the generated evaluator is not of type BoolEvaluator")
	}
	if pEvalBool.EvalFnc == nil {
		pEvalBool.EvalFnc = func(ctx *Context) bool {
			return pEvalBool.Value
		}
	}
	// Insert partial evaluators in the rule
	evaluator.SetPartial(field, pEvalBool.EvalFnc)
}

func eval(t *testing.T, event *testEvent, expr string) (bool, *ast.Rule, error) {
	model := &testModel{event: event}

	ctx := &Context{}

	opts := NewOptsWithParams(false, testConstants)
	evaluator, rule, err := parse(t, expr, model, &opts, nil)
	if err != nil {
		return false, rule, err
	}
	r1 := evaluator.Eval(ctx)

	opts = NewOptsWithParams(true, testConstants)
	evaluator, _, err = parse(t, expr, model, &opts, nil)
	if err != nil {
		return false, rule, err
	}
	r2 := evaluator.Eval(ctx)

	if r1 != r2 {
		t.Fatalf("different result for non-debug and debug evalutators with rule `%s`", expr)
	}

	return r1, rule, nil
}

func TestStringError(t *testing.T) {
	event := &testEvent{
		process: testProcess{
			name: "/usr/bin/cat",
			uid:  1,
		},
		open: testOpen{
			filename: "/etc/shadow",
		},
	}

	_, _, err := eval(t, event, `process.name != "/usr/bin/vipw" && process.uid != 0 && open.filename == 3`)
	if err == nil || err.(*AstToEvalError).Pos.Column != 73 {
		t.Fatal("should report a string type error")
	}
}

func TestIntError(t *testing.T) {
	event := &testEvent{
		process: testProcess{
			name: "/usr/bin/cat",
			uid:  1,
		},
		open: testOpen{
			filename: "/etc/shadow",
		},
	}

	_, _, err := eval(t, event, `process.name != "/usr/bin/vipw" && process.uid != "test" && Open.Filename == "/etc/shadow"`)
	if err == nil || err.(*AstToEvalError).Pos.Column != 51 {
		t.Fatal("should report a string type error")
	}
}

func TestBoolError(t *testing.T) {
	event := &testEvent{
		process: testProcess{
			name: "/usr/bin/cat",
			uid:  1,
		},
		open: testOpen{
			filename: "/etc/shadow",
		},
	}

	_, _, err := eval(t, event, `(process.name != "/usr/bin/vipw") == "test"`)
	if err == nil || err.(*AstToEvalError).Pos.Column != 38 {
		t.Fatal("should report a bool type error")
	}
}

func TestSimpleString(t *testing.T) {
	event := &testEvent{
		process: testProcess{
			name: "/usr/bin/cat",
			uid:  1,
		},
	}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `process.name != "/usr/bin/vipw"`, Expected: true},
		{Expr: `process.name != "/usr/bin/cat"`, Expected: false},
		{Expr: `process.name == "/usr/bin/cat"`, Expected: true},
		{Expr: `process.name == "/usr/bin/vipw"`, Expected: false},
		{Expr: `(process.name == "/usr/bin/cat" && process.uid == 0) && (process.name == "/usr/bin/cat" && process.uid == 0)`, Expected: false},
		{Expr: `(process.name == "/usr/bin/cat" && process.uid == 1) && (process.name == "/usr/bin/cat" && process.uid == 1)`, Expected: true},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s`: %s", test.Expr, err)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestSimpleInt(t *testing.T) {
	event := &testEvent{
		process: testProcess{
			uid: 444,
		},
	}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `111 != 555`, Expected: true},
		{Expr: `process.uid != 555`, Expected: true},
		{Expr: `process.uid != 444`, Expected: false},
		{Expr: `process.uid == 444`, Expected: true},
		{Expr: `process.uid == 555`, Expected: false},
		{Expr: `--3 == 3`, Expected: true},
		{Expr: `3 ^ 3 == 0`, Expected: true},
		{Expr: `^0 == -1`, Expected: true},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s`: %s", test.Expr, err)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestSimpleBool(t *testing.T) {
	event := &testEvent{}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `(444 == 444) && ("test" == "test")`, Expected: true},
		{Expr: `(444 != 444) && ("test" == "test")`, Expected: false},
		{Expr: `(444 != 555) && ("test" == "test")`, Expected: true},
		{Expr: `(444 != 555) && ("test" != "aaaa")`, Expected: true},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s`: %s", test.Expr, err)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestPrecedence(t *testing.T) {
	event := &testEvent{}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `false || (true != true)`, Expected: false},
		{Expr: `false || true`, Expected: true},
		{Expr: `1 == 1 & 1`, Expected: true},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s`: %s", test.Expr, err)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestParenthesis(t *testing.T) {
	event := &testEvent{}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `(true) == (true)`, Expected: true},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s`: %s", test.Expr, err)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestSimpleBitOperations(t *testing.T) {
	event := &testEvent{}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `(3 & 3) == 3`, Expected: true},
		{Expr: `(3 & 1) == 3`, Expected: false},
		{Expr: `(2 | 1) == 3`, Expected: true},
		{Expr: `(3 & 1) != 0`, Expected: true},
		{Expr: `0 != 3 & 1`, Expected: true},
		{Expr: `(3 ^ 3) == 0`, Expected: true},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s`", test.Expr)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestRegexp(t *testing.T) {
	event := &testEvent{
		process: testProcess{
			name: "/usr/bin/c$t",
		},
	}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `process.name =~ "/usr/bin/*"`, Expected: true},
		{Expr: `process.name =~ "/usr/sbin/*"`, Expected: false},
		{Expr: `process.name !~ "/usr/sbin/*"`, Expected: true},
		{Expr: `process.name =~ "/bin/"`, Expected: false},
		{Expr: `process.name =~ "/bin/*"`, Expected: false},
		{Expr: `process.name =~ ""`, Expected: false},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s`: %s", test.Expr, err)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestInArray(t *testing.T) {
	event := &testEvent{
		process: testProcess{
			name: "a",
			uid:  3,
		},
	}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `"a" in [ "a", "b", "c" ]`, Expected: true},
		{Expr: `process.name in [ "c", "b", "a" ]`, Expected: true},
		{Expr: `"d" in [ "a", "b", "c" ]`, Expected: false},
		{Expr: `process.name in [ "c", "b", "z" ]`, Expected: false},
		{Expr: `"a" not in [ "a", "b", "c" ]`, Expected: false},
		{Expr: `process.name not in [ "c", "b", "a" ]`, Expected: false},
		{Expr: `"d" not in [ "a", "b", "c" ]`, Expected: true},
		{Expr: `process.name not in [ "c", "b", "z" ]`, Expected: true},
		{Expr: `3 in [ 1, 2, 3 ]`, Expected: true},
		{Expr: `process.uid in [ 1, 2, 3 ]`, Expected: true},
		{Expr: `4 in [ 1, 2, 3 ]`, Expected: false},
		{Expr: `process.uid in [ 4, 2, 1 ]`, Expected: false},
		{Expr: `3 not in [ 1, 2, 3 ]`, Expected: false},
		{Expr: `3 not in [ 1, 2, 3 ]`, Expected: false},
		{Expr: `4 not in [ 1, 2, 3 ]`, Expected: true},
		{Expr: `4 not in [ 3, 2, 1 ]`, Expected: true},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s: %s`", test.Expr, err)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestComplex(t *testing.T) {
	event := &testEvent{
		open: testOpen{
			filename: "/var/lib/httpd/htpasswd",
			flags:    syscall.O_CREAT | syscall.O_TRUNC | syscall.O_EXCL | syscall.O_RDWR | syscall.O_WRONLY,
		},
	}

	tests := []struct {
		Expr     string
		Expected bool
	}{
		{Expr: `open.filename =~ "/var/lib/httpd/*" && open.flags & (O_CREAT | O_TRUNC | O_EXCL | O_RDWR | O_WRONLY) > 0`, Expected: true},
	}

	for _, test := range tests {
		result, _, err := eval(t, event, test.Expr)
		if err != nil {
			t.Fatalf("error while evaluating `%s: %s`", test.Expr, err)
		}

		if result != test.Expected {
			t.Errorf("expected result `%t` not found, got `%t`\n%s", test.Expected, result, test.Expr)
		}
	}
}

func TestTags(t *testing.T) {
	expr := `process.name != "/usr/bin/vipw" && open.filename == "/etc/passwd"`
	evaluator, _, err := parse(t, expr, &testModel{}, &Opts{}, nil)
	if err != nil {
		t.Fatal(fmt.Sprintf("%s\n%s", err, expr))
	}

	expected := []string{"fs", "process"}

	if !reflect.DeepEqual(evaluator.Tags, expected) {
		t.Errorf("tags expected not %+v != %+v", expected, evaluator.Tags)
	}
}

func TestPartial(t *testing.T) {
	event := testEvent{
		process: testProcess{
			name:   "abc",
			uid:    123,
			isRoot: true,
		},
		open: testOpen{
			filename: "xyz",
		},
	}

	tests := []struct {
		Expr        string
		Field       string
		IsDiscarder bool
	}{
		{Expr: `true || process.name == "/usr/bin/cat"`, Field: "process.name", IsDiscarder: false},
		{Expr: `false || process.name == "/usr/bin/cat"`, Field: "process.name", IsDiscarder: true},
		{Expr: `true || process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `false || process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `true && process.name == "/usr/bin/cat"`, Field: "process.name", IsDiscarder: true},
		{Expr: `false && process.name == "/usr/bin/cat"`, Field: "process.name", IsDiscarder: true},
		{Expr: `true && process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `false && process.name == "abc"`, Field: "process.name", IsDiscarder: true},
		{Expr: `open.filename == "test1" && process.name == "/usr/bin/cat"`, Field: "process.name", IsDiscarder: true},
		{Expr: `open.filename == "test1" && process.name != "/usr/bin/cat"`, Field: "process.name", IsDiscarder: false},
		{Expr: `open.filename == "test1" || process.name == "/usr/bin/cat"`, Field: "process.name", IsDiscarder: false},
		{Expr: `open.filename == "test1" || process.name != "/usr/bin/cat"`, Field: "process.name", IsDiscarder: false},
		{Expr: `open.filename == "test1" && !(process.name == "/usr/bin/cat")`, Field: "process.name", IsDiscarder: false},
		{Expr: `open.filename == "test1" && !(process.name != "/usr/bin/cat")`, Field: "process.name", IsDiscarder: true},
		{Expr: `open.filename == "test1" && (process.name =~ "/usr/bin/*" )`, Field: "process.name", IsDiscarder: true},
		{Expr: `open.filename == "test1" && process.name =~ "ab*" `, Field: "process.name", IsDiscarder: false},
		{Expr: `open.filename == "test1" && process.name == open.filename`, Field: "process.name", IsDiscarder: false},
		{Expr: `open.filename =~ "test1" && process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `open.filename in [ "test1", "test2" ] && (process.name == open.filename)`, Field: "process.name", IsDiscarder: false},
		{Expr: `open.filename in [ "test1", "test2" ] && process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `!(open.filename in [ "test1", "test2" ]) && process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `!(open.filename in [ "test1", "xyz" ]) && process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `!(open.filename in [ "test1", "xyz" ]) && process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `!(open.filename in [ "test1", "xyz" ] && true) && process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `!(open.filename in [ "test1", "xyz" ] && false) && process.name == "abc"`, Field: "process.name", IsDiscarder: false},
		{Expr: `!(open.filename in [ "test1", "xyz" ] && false) && !(process.name == "abc")`, Field: "process.name", IsDiscarder: true},
		{Expr: `!(open.filename in [ "test1", "xyz" ] && false) && !(process.name == "abc")`, Field: "open.filename", IsDiscarder: false},
		{Expr: `(open.filename not in [ "test1", "xyz" ] && true) && !(process.name == "abc")`, Field: "open.filename", IsDiscarder: true},
		{Expr: `open.filename == open.filename`, Field: "open.filename", IsDiscarder: false},
		{Expr: `open.filename != open.filename`, Field: "open.filename", IsDiscarder: true},
		{Expr: `open.filename == "test1" && process.uid == 456`, Field: "process.uid", IsDiscarder: true},
		{Expr: `open.filename == "test1" && process.uid == 123`, Field: "process.uid", IsDiscarder: false},
		{Expr: `open.filename == "test1" && !process.is_root`, Field: "process.is_root", IsDiscarder: true},
		{Expr: `open.filename == "test1" && process.is_root`, Field: "process.is_root", IsDiscarder: false},
	}

	for _, test := range tests {
		model := &testModel{event: &event}
		opts := &Opts{Constants: testConstants}
		evaluator, rule, err := parse(t, test.Expr, model, opts, nil)
		if err != nil {
			t.Fatalf("error while evaluating `%s`: %s", test.Expr, err)
		}
		generatePartials(t, test.Field, model, opts, evaluator, rule, nil)

		result, err := evaluator.PartialEval(&Context{}, test.Field)
		if err != nil {
			t.Fatalf("error while partial evaluating `%s` for `%s`: %s", test.Expr, test.Field, err)
		}

		if !result != test.IsDiscarder {
			t.Fatalf("expected result `%t` for `%s`, got `%t`\n%s", test.IsDiscarder, test.Field, result, test.Expr)
		}
	}
}

func TestMacroList(t *testing.T) {
	expr := `[ "/etc/shadow", "/etc/password" ]`

	macro, err := ast.ParseMacro(expr)
	if err != nil {
		t.Fatalf("%s\n%s", err, expr)
	}

	macros := map[string]*ast.Macro{
		"list": macro,
	}

	expr = `"/etc/shadow" in list`
	opts := NewOptsWithParams(false, make(map[string]interface{}))
	evaluator, _, err := parse(t, expr, &testModel{event: &testEvent{}}, &opts, macros)
	if err != nil {
		t.Fatalf("error while evaluating `%s`: %s", expr, err)
	}

	if !evaluator.Eval(&Context{}) {
		t.Fatalf("should return true")
	}
}

func TestMacroExpression(t *testing.T) {
	expr := `open.filename in [ "/etc/shadow", "/etc/passwd" ]`

	macro, err := ast.ParseMacro(expr)
	if err != nil {
		t.Fatalf("%s\n%s", err, expr)
	}

	macros := map[string]*ast.Macro{
		"is_passwd": macro,
	}

	event := testEvent{
		process: testProcess{
			name: "httpd",
		},
		open: testOpen{
			filename: "/etc/passwd",
		},
	}

	expr = `process.name == "httpd" && is_passwd`
	opts := NewOptsWithParams(false, make(map[string]interface{}))
	evaluator, _, err := parse(t, expr, &testModel{event: &event}, &opts, macros)
	if err != nil {
		t.Fatalf("error while evaluating `%s`: %s", expr, err)
	}

	if !evaluator.Eval(&Context{}) {
		t.Fatalf("should return true")
	}
}

func TestMacroPartial(t *testing.T) {
	expr := `open.filename in [ "/etc/shadow", "/etc/passwd" ]`

	macro, err := ast.ParseMacro(expr)
	if err != nil {
		t.Fatalf("%s\n%s", err, expr)
	}

	macros := map[string]*ast.Macro{
		"is_passwd": macro,
	}

	event := testEvent{
		open: testOpen{
			filename: "/etc/hosts",
		},
	}

	expr = `is_passwd`
	model := &testModel{event: &event}
	opts := NewOptsWithParams(false, make(map[string]interface{}))
	field := "open.filename"

	evaluator, rule, err := parse(t, expr, model, &opts, macros)
	if err != nil {
		t.Fatalf("error while evaluating `%s`: %s", expr, err)
	}

	// generate rule partials
	generatePartials(t, field, model, &opts, evaluator, rule, macros)

	result, err := evaluator.PartialEval(&Context{}, field)
	if err != nil {
		t.Fatal(err)
	}

	if result {
		t.Fatal("should be a discriminator")
	}

	model.event.open.filename = "/etc/passwd"
	if !evaluator.Eval(&Context{}) {
		t.Fatalf("should return true")
	}
}

func TestNestedMacros(t *testing.T) {
	macro1Expr := `[ "/etc/shadow", "/etc/passwd" ]`
	macro2Expr := `open.filename in sensitive_files`

	macro1, err := ast.ParseMacro(macro1Expr)
	if err != nil {
		t.Fatalf("%s\n%s", err, macro1Expr)
	}

	macro2, err := ast.ParseMacro(macro2Expr)
	if err != nil {
		t.Fatalf("%s\n%s", err, macro2Expr)
	}

	macros := map[string]*ast.Macro{
		"sensitive_files": macro1,
		"is_passwd":       macro2,
	}

	event := testEvent{
		open: testOpen{
			filename: "/etc/hosts",
		},
	}

	ruleExpr := `is_passwd`
	model := &testModel{event: &event}
	opts := NewOptsWithParams(false, make(map[string]interface{}))
	field := "open.filename"

	evaluator, rule, err := parse(t, ruleExpr, model, &opts, macros)
	if err != nil {
		t.Fatalf("error while evaluating `%s`: %s", ruleExpr, err)
	}

	// generate rule partials
	generatePartials(t, field, model, &opts, evaluator, rule, macros)

	result, err := evaluator.PartialEval(&Context{}, field)
	if err != nil {
		t.Fatal(err)
	}

	if result {
		t.Error("should be a discriminator")
	}

}

func BenchmarkComplex(b *testing.B) {
	event := testEvent{
		process: testProcess{
			name: "/usr/bin/ls",
			uid:  1,
		},
	}

	ctx := &Context{}

	base := `(process.name == "/usr/bin/ls" && process.uid == 1)`
	var exprs []string

	for i := 0; i != 100; i++ {
		exprs = append(exprs, base)
	}

	expr := strings.Join(exprs, " && ")

	rule, err := ast.ParseRule(expr)
	if err != nil {
		b.Fatal(fmt.Sprintf("%s\n%s", err, expr))
	}

	evaluator, err := RuleToEvaluator(rule, &testModel{event: &event}, &Opts{})
	if err != nil {
		b.Fatal(fmt.Sprintf("%s\n%s", err, expr))
	}

	for i := 0; i < b.N; i++ {
		if evaluator.Eval(ctx) != true {
			b.Fatal("unexpected result")
		}
	}
}

func BenchmarkPartial(b *testing.B) {
	event := testEvent{
		process: testProcess{
			name: "abc",
			uid:  1,
		},
	}

	ctx := &Context{}

	base := `(process.name == "/usr/bin/ls" && process.uid != 0)`
	var exprs []string

	for i := 0; i != 100; i++ {
		exprs = append(exprs, base)
	}

	expr := strings.Join(exprs, " && ")

	rule, err := ast.ParseRule(expr)
	if err != nil {
		b.Fatal(fmt.Sprintf("%s\n%s", err, expr))
	}

	evaluator, err := RuleToEvaluator(rule, &testModel{event: &event}, &Opts{})
	if err != nil {
		b.Fatal(fmt.Sprintf("%s\n%s", err, expr))
	}

	for i := 0; i < b.N; i++ {
		if ok, _ := evaluator.PartialEval(ctx, "process.name"); ok {
			b.Fatal("unexpected result")
		}
	}
}
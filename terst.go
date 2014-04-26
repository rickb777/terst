/*
Package terst is a terse (terst = test + terse), easy-to-use testing library for Go.

terst is compatible with (and works via) the standard testing package: http://golang.org/pkg/testing

    var is = terst.Is

    func Test(t *testing.T) {
        terst.Terst(t, func() {
            is("abc", "abc")

            is(1, ">", 0)

            var abc []int
            is(abc, nil)
        }
    }

Do not import terst directly, instead use `terst-import` to copy it into your testing environment:

https://github.com/robertkrimen/terst/tree/master/terst-import

    $ go get github.com/robertkrimen/terst/terst-import

    $ terst-import

*/
package terst

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Is compares two values (got & expect) and returns true if the comparison is true,
// false otherwise. In addition, if the comparison is false, Is will report the error
// in a manner similar to testing.T.Error(...). Is also takes an optional argument,
// a comparator, that changes how the comparison is made.  The following
// comparators are available:
//
//      ==      # got == expect (default)
//      !=      # got != expect
//
//      >       # got > expect (float32, uint, uint16, int, int64, ...)
//      >=      # got >= expect
//      <       # got < expect
//      <=      # got <= expect
//
//      =~      # regexp.MustCompile(expect).Match{String}(got)
//      !~      # !regexp.MustCompile(expect).Match{String}(got)
//
// Basic usage with the default comparator (`==`):
//
//      Is(<got>, <expect>)
//
// Specifying a different comparator:
//
//      Is(<got>, <comparator>, <expect>)
//
// A simple comparison:
//
//      Is(2 + 2, 4)
//
// A bit trickier:
//
//      Is(1, ">", 0)
//      Is(2 + 2, "!=", 5)
//      Is("Nothing happens.", "=~", `ing(\s+)happens\.$`)
//
// Is should only be called under a Terst(t, ...) call. For a standalone version,
// use IsErr(...). If no scope is found and the comparison is false, then Is will panic the error.
//
func Is(arguments ...interface{}) bool {
	err := IsErr(arguments...)
	if err != nil {
		call := Caller()
		if call == nil {
			panic(err)
		}
		call.Error(err)
		return false
	}
	return true
}

type (
	// ErrFail indicates a comparison failure (e.g. 0 > 1).
	ErrFail error

	// ErrInvalid indicates an invalid comparison (e.g. bool == string).
	ErrInvalid error
)

var errInvalid = errors.New("invalid")

var registry = struct {
	table map[uintptr]*_scope
	lock  sync.RWMutex
}{
	table: map[uintptr]*_scope{},
}

func registerScope(pc uintptr, scope *_scope) {
	registry.lock.Lock()
	defer registry.lock.Unlock()
	registry.table[pc] = scope
}

func scope() *_scope {
	scope, _ := findScope()
	return scope
}

func floatCompare(a float64, b float64) int {
	if a > b {
		return 1
	} else if a < b {
		return -1
	}
	// NaN == NaN
	return 0
}

func bigIntCompare(a *big.Int, b *big.Int) int {
	return a.Cmp(b)
}

func bigInt(value int64) *big.Int {
	return big.NewInt(value)
}

func bigUint(value uint64) *big.Int {
	return big.NewInt(0).SetUint64(value)
}

type _toString interface {
	String() string
}

func toString(value interface{}) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case _toString:
		return value.String(), nil
	case error:
		return value.Error(), nil
	}
	return "", errInvalid
}

func match(got string, expect *regexp.Regexp) (int, error) {
	if expect.MatchString(got) {
		return 0, nil
	}
	return -1, nil
}

func compareMatch(got, expect interface{}) (int, error) {
	if got, err := toString(got); err != nil {
		switch expect := expect.(type) {
		case string:
			{
				expect := regexp.MustCompile(expect)
				return match(got, expect)
			}
		case *regexp.Regexp:
			return match(got, expect)
		default:
			goto invalid
		}
	} else {
		return 0, err
	}
invalid:
	return 0, errInvalid
}

func floatPromote(value reflect.Value) (float64, error) {
	kind := value.Kind()
	if reflect.Int <= kind && kind <= reflect.Int64 {
		return float64(value.Int()), nil
	}
	if reflect.Uint <= kind && kind <= reflect.Uint64 {
		return float64(value.Uint()), nil
	}
	if reflect.Float32 <= kind && kind <= reflect.Float64 {
		return value.Float(), nil
	}
	return 0, errInvalid
}

func bigIntPromote(value reflect.Value) (*big.Int, error) {
	kind := value.Kind()
	if reflect.Int <= kind && kind <= reflect.Int64 {
		return bigInt(value.Int()), nil
	}
	if reflect.Uint <= kind && kind <= reflect.Uint64 {
		return bigUint(value.Uint()), nil
	}
	return nil, errInvalid
}

func compareOther(got, expect interface{}) (int, error) {
	{
		switch expect.(type) {
		case float32, float64:
			return compareNumber(got, expect)
		case uint, uint8, uint16, uint32, uint64:
			return compareNumber(got, expect)
		case int, int8, int16, int32, int64:
			return compareNumber(got, expect)
		case string:
			var err error
			got, err = toString(got)
			if err != nil {
				return 0, err
			}
		case nil:
			got := reflect.ValueOf(got)
			switch got.Kind() {
			case reflect.Chan, reflect.Func, reflect.Map, reflect.Ptr, reflect.Slice, reflect.Interface:
				if got.IsNil() {
					return 0, nil
				}
				return -1, nil
			case reflect.Invalid: // reflect.Invalid: var abc interface{} = nil
				return 0, nil
			}
			return 0, errInvalid
		}
	}

	if reflect.ValueOf(got).Type() != reflect.ValueOf(expect).Type() {
		return 0, errInvalid
	}

	if reflect.DeepEqual(got, expect) {
		return 0, nil
	}
	return -1, nil
}

func compareNumber(got, expect interface{}) (int, error) {
	{
		got := reflect.ValueOf(got)
		k0 := got.Kind()
		expect := reflect.ValueOf(expect)
		k1 := expect.Kind()
		if reflect.Float32 <= k0 && k0 <= reflect.Float64 ||
			reflect.Float32 <= k1 && k1 <= reflect.Float64 {
			got, err := floatPromote(got)
			if err != nil {
				return 0, err
			}
			expect, err := floatPromote(expect)
			if err != nil {
				return 0, err
			}
			return floatCompare(got, expect), nil
		} else {
			got, err := bigIntPromote(got)
			if err != nil {
				return 0, err
			}
			expect, err := bigIntPromote(expect)
			if err != nil {
				return 0, err
			}
			return got.Cmp(expect), nil
		}
	}

	return 0, errInvalid
}

// IsErr compares two values (got & expect) and returns nil if the comparison is true, an ErrFail if
// the comparison is false, or an ErrInvalid if the comparison is invalid. IsErr also
// takes an optional argument, a comparator, that changes how the comparison is made.
//
func IsErr(arguments ...interface{}) error {
	var got, expect interface{}
	comparator := "=="
	switch len(arguments) {
	case 0, 1:
		return fmt.Errorf("invalid number of arguments to IsErr: %d", len(arguments))
	case 2:
		got, expect = arguments[0], arguments[1]
	default:
		if value, ok := arguments[1].(string); ok {
			comparator = value
		} else {
			return fmt.Errorf("invalid comparator: %v", arguments[1])
		}
		got, expect = arguments[0], arguments[2]
	}

	var result int
	var err error

	switch comparator {
	case "<", "<=", ">", ">=":
		result, err = compareNumber(got, expect)
	case "=~", "!~":
		result, err = compareMatch(got, expect)
	case "==", "!=":
		result, err = compareOther(got, expect)
	default:
		return fmt.Errorf("invalid comparator: %s", comparator)
	}

	if err == errInvalid {
		return ErrInvalid(fmt.Errorf(
			"\nINVALID (%s):\n        got: %v (%T)\n   expected: %v (%T)",
			comparator,
			got, got,
			expect, expect,
		))
	} else if err != nil {
		return err
	}

	equality, pass := false, false

	switch comparator {
	case "==", "=~":
		equality = true
		pass = result == 0
	case "!=", "!~":
		equality = true
		pass = result != 0
	case "<":
		pass = result < 0
	case "<=":
		pass = result <= 0
	case ">":
		pass = result > 0
	case ">=":
		pass = result >= 0
	}

	if !pass {
		if equality {
			return ErrFail(fmt.Errorf(
				"\nFAIL (%s)\n     got: %v%s\nexpected: %v%s",
				comparator,
				got, typeKindString(got),
				expect, typeKindString(expect),
			))
		}
		return ErrFail(fmt.Errorf(
			"\nFAIL (%s)\n     got: %v%s\nexpected: %s %v%s",
			comparator,
			got, typeKindString(got),
			comparator, expect, typeKindString(expect),
		))
	}

	return nil
}

func typeKindString(value interface{}) string {
	reflectValue := reflect.ValueOf(value)
	kind := reflectValue.Kind().String()
	result := fmt.Sprintf("%T", value)
	if kind == result {
		if kind == "string" {
			return ""
		}
		return fmt.Sprintf(" (%T)", value)
	}
	return fmt.Sprintf(" (%T=%s)", value, kind)
}

func (scope *_scope) reset() {
	scope.name = ""
	scope.output = scope.output[:]
	scope.start = time.Time{}
	scope.duration = 0
}

// Terst creates a testing scope, where Is can be called and errors will be reported
// according to the top-level location of the comparison, and not where the Is call
// actually takes place. For example:
//
//      func test() {
//          Is(2 + 2, 5) // <--- This failure is reported below.
//      }
//
//      Terst(t, func(){
//
//          Is(2, ">", 3) // <--- An error is reported here.
//
//          test() // <--- An error is reported here.
//
//      })
//
func Terst(t *testing.T, arguments ...func()) {
	scope := &_scope{
		t: t,
	}

	pc, _, _, ok := runtime.Caller(1) // TODO Associate with the Test... func
	if !ok {
		panic("Here be dragons.")
	}

	_, scope.testFunc = findTestFunc()

	registerScope(pc, scope)

	for _, fn := range arguments {
		func() {
			scope.reset()
			scope.name = "-"
			scope.start = time.Now()
			defer func() {
				scope.duration = time.Now().Sub(scope.start)
				if err := recover(); err != nil {
					scope.t.Fail()
					scope.report()
					panic(err)
				}
				scope.report()
			}()
			fn()
		}()
	}
}

// From "testing"
func (scope *_scope) report() {
	tstr := fmt.Sprintf("(%.2f seconds)", scope.duration.Seconds())
	format := "--- %s: %s %s\n%s"
	if scope.t.Failed() {
		fmt.Printf(format, "FAIL", scope.name, tstr, scope.output)
	} else if testing.Verbose() {
		fmt.Printf(format, "PASS", scope.name, tstr, scope.output)
	}
}

func (scope *_scope) log(call _entry, str string) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.output = append(scope.output, decorate(call, str)...)
}

// decorate prefixes the string with the file and line of the call site
// and inserts the final newline if needed and indentation tabs for formascing.
func decorate(call _entry, s string) string {

	file, line := call.File, call.Line
	if call.PC > 0 {
		// Truncate file name at last file name separator.
		if index := strings.LastIndex(file, "/"); index >= 0 {
			file = file[index+1:]
		} else if index = strings.LastIndex(file, "\\"); index >= 0 {
			file = file[index+1:]
		}
	} else {
		file = "???"
		line = 1
	}
	buf := new(bytes.Buffer)
	// Every line is indented at least one tab.
	buf.WriteByte('\t')
	fmt.Fprintf(buf, "%s:%d: ", file, line)
	lines := strings.Split(s, "\n")
	if l := len(lines); l > 1 && lines[l-1] == "" {
		lines = lines[:l-1]
	}
	for i, line := range lines {
		if i > 0 {
			// Second and subsequent lines are indented an extra tab.
			buf.WriteString("\n\t\t")
		}
		buf.WriteString(line)
	}
	buf.WriteByte('\n')
	return buf.String()
}

func findScope() (*_scope, _entry) {
	registry.lock.RLock()
	defer registry.lock.RUnlock()
	table := registry.table
	depth := 2 // Starting depth
	call := _entry{}
	for {
		pc, _, _, ok := runtime.Caller(depth)
		if !ok {
			break
		}
		if scope, exists := table[pc]; exists {
			pc, file, line, _ := runtime.Caller(depth - 3) // Terst(...) + func(){}() + fn() => ???()
			call.PC = pc
			call.File = file
			call.Line = line
			return scope, call
		}
		depth++
	}
	return nil, _entry{}
}

// Call is a reference to a line immediately under a Terst testing scope.
type Call struct {
	scope *_scope
	entry _entry
}

// Caller will search the stack, looking for a Terst testing scope. If a scope
// is found, then Caller returns a Call for logging errors, accessing testing.T, etc.
// If no scope is found, Caller returns nil.
func Caller() *Call {
	scope, entry := findScope()
	if scope == nil {
		return nil
	}
	return &Call{
		scope: scope,
		entry: entry,
	}
}

// TestFunc returns the *runtime.Func entry for the top-level Test...(t testing.T)
// function.
func (cl *Call) TestFunc() *runtime.Func {
	return cl.scope.testFunc
}

// T returns the original testing.T passed to Terst(...)
func (cl *Call) T() *testing.T {
	return cl.scope.t
}

// Log is the terst version of `testing.T.Log`
func (cl *Call) Log(arguments ...interface{}) {
	cl.scope.log(cl.entry, fmt.Sprintln(arguments...))
}

// Logf is the terst version of `testing.T.Logf`
func (cl *Call) Logf(format string, arguments ...interface{}) {
	cl.scope.log(cl.entry, fmt.Sprintf(format, arguments...))
}

// Error is the terst version of `testing.T.Error`
func (cl *Call) Error(arguments ...interface{}) {
	cl.scope.log(cl.entry, fmt.Sprintln(arguments...))
	cl.scope.t.Fail()
}

// Errorf is the terst version of `testing.T.Errorf`
func (cl *Call) Errorf(format string, arguments ...interface{}) {
	cl.scope.log(cl.entry, fmt.Sprintf(format, arguments...))
	cl.scope.t.Fail()
}

// Skip is the terst version of `testing.T.Skip`
func (cl *Call) Skip(arguments ...interface{}) {
	cl.scope.log(cl.entry, fmt.Sprintln(arguments...))
	cl.scope.t.SkipNow()
}

// Skipf is the terst version of `testing.T.Skipf`
func (cl *Call) Skipf(format string, arguments ...interface{}) {
	cl.scope.log(cl.entry, fmt.Sprintf(format, arguments...))
	cl.scope.t.SkipNow()
}

type _scope struct {
	t        *testing.T
	testFunc *runtime.Func
	name     string
	mu       sync.RWMutex
	output   []byte
	start    time.Time
	duration time.Duration
}

type _entry struct {
	PC   uintptr
	File string
	Line int
	Func *runtime.Func
}

func _findFunc(match string) (_entry, *runtime.Func) {
	depth := 2 // Starting depth
	for {
		pc, file, line, ok := runtime.Caller(depth)
		if !ok {
			break
		}
		fn := runtime.FuncForPC(pc)
		name := fn.Name()
		if index := strings.LastIndex(name, match); index >= 0 {
			// Assume we have an instance of TestXyzzy in a _test file
			return _entry{
				PC:   pc,
				File: file,
				Line: line,
				Func: fn,
			}, fn
		}
		depth++
	}
	return _entry{}, nil
}

func findTestFunc() (_entry, *runtime.Func) {
	return _findFunc(".Test")
}

func findTerstFunc() (_entry, *runtime.Func) {
	return _findFunc(".Terst")
}

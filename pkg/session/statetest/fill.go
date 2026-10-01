// Package statetest helps tests prove that every field of a persisted
// settings struct survives a save/restore, including fields added later.
package statetest

import (
	"reflect"
	"testing"
)

// FillNonZero sets every field of the struct ptr points to (recursing into
// nested structs) to a non-zero value chosen by type alone, so a field added
// later is covered with no test change. Strings get str; a field whose type
// matches one of special gets that value. It fails t on a type it cannot fill.
func FillNonZero(t testing.TB, ptr any, str string, special ...any) {
	t.Helper()
	fill(t, reflect.ValueOf(ptr).Elem(), str, special)
}

func fill(t testing.TB, v reflect.Value, str string, special []any) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := v.Type().Field(i).Name
		if !f.CanSet() {
			continue
		}
		set := false
		for _, sp := range special {
			if reflect.TypeOf(sp) == f.Type() {
				f.Set(reflect.ValueOf(sp))
				set = true
				break
			}
		}
		if !set {
			switch {
			case f.Kind() == reflect.Struct:
				fill(t, f, str, special)
			case f.Kind() == reflect.String:
				f.SetString(str)
			case f.Kind() == reflect.Bool:
				f.SetBool(true)
			case f.CanInt():
				f.SetInt(7)
			case f.CanFloat():
				f.SetFloat(7)
			case f.Kind() == reflect.Map && f.Type().Key().Kind() == reflect.String &&
				(f.Type().Elem().Kind() == reflect.Interface || f.Type().Elem().Kind() == reflect.String):
				m := reflect.MakeMap(f.Type())
				elem := reflect.New(f.Type().Elem()).Elem()
				elem.Set(reflect.ValueOf("v"))
				m.SetMapIndex(reflect.ValueOf("k"), elem)
				f.Set(m)
			case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.String:
				f.Set(reflect.ValueOf([]string{"v"}).Convert(f.Type()))
			default:
				t.Fatalf("statetest.FillNonZero: teach it field %s of type %s", name, f.Type())
			}
		}
		if f.IsZero() {
			t.Fatalf("statetest.FillNonZero left %s zero", name)
		}
	}
}

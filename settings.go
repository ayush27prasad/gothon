package gothon

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// SettingsEnv turns name/value pairs into KEY=VALUE entries for Config.Env. It backs
// the Env method of generated Settings types.
//
// A nil pointer or a zero non-pointer value is left out, so Python falls back to its
// default and reports required settings that were never filled in. A non-nil
// pointer is always written, even when it points to a zero value. Strings,
// numbers, booleans and times are written as text; anything else as JSON, which
// pydantic-settings parses for complex fields.
func SettingsEnv(pairs ...any) []string {
	var env []string
	for i := 0; i+1 < len(pairs); i += 2 {
		v := reflect.ValueOf(pairs[i+1])
		set := v.IsValid() && !v.IsZero()
		if v.Kind() == reflect.Pointer {
			set = !v.IsNil()
			v = v.Elem()
		}
		if set {
			env = append(env, fmt.Sprint(pairs[i])+"="+envValue(v))
		}
	}
	return env
}

func envValue(v reflect.Value) string {
	if t, ok := v.Interface().(time.Time); ok {
		return t.Format(time.RFC3339Nano)
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	}
	b, _ := json.Marshal(v.Interface())
	return string(b)
}

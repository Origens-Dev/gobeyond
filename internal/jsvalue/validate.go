// Package jsvalue validates values before Go renders and JSON serializes them
// for the pinned JavaScript runtime.
package jsvalue

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"
)

const maxSafeInteger = uint64(1<<53 - 1)
const maxCollectionItems = 10_000
const maxValueNodes = 100_000
const maxStringBytes = 16 << 20

type budget struct {
	nodes       int
	stringBytes int
}

// Validate rejects Go values whose JSON representation cannot preserve the
// same observable value in JavaScript. Nil collections are rejected because
// generated array/object contracts expose collections, not null.
func Validate(value any) error {
	return validate(reflect.ValueOf(value), "$", 0, &budget{})
}

func validate(value reflect.Value, path string, depth int, limits *budget) error {
	if depth > 100 {
		return errors.New("JavaScript value exceeds maximum nesting depth")
	}
	if !value.IsValid() {
		return nil
	}
	limits.nodes++
	if limits.nodes > maxValueNodes {
		return errors.New("JavaScript value exceeds node budget")
	}
	if value.CanInterface() {
		if _, ok := value.Interface().(json.Marshaler); ok {
			return nil
		}
		if _, ok := value.Interface().(time.Time); ok {
			return nil
		}
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.String:
		limits.stringBytes += value.Len()
		if limits.stringBytes > maxStringBytes {
			return errors.New("JavaScript value exceeds string-byte budget")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number := value.Int()
		if number > int64(maxSafeInteger) || number < -int64(maxSafeInteger) {
			return fmt.Errorf("%s integer is outside JavaScript's safe range", path)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if value.Uint() > maxSafeInteger {
			return fmt.Errorf("%s integer is outside JavaScript's safe range", path)
		}
	case reflect.Float32, reflect.Float64:
		if number := value.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("%s must be a finite JSON number", path)
		}
	case reflect.Slice:
		if value.IsNil() {
			return fmt.Errorf("%s required collection is nil", path)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			limits.stringBytes += value.Len()
			if limits.stringBytes > maxStringBytes {
				return errors.New("JavaScript value exceeds byte budget")
			}
			return nil
		}
		if value.Len() > maxCollectionItems {
			return fmt.Errorf("%s exceeds collection item budget", path)
		}
		for index := 0; index < value.Len(); index++ {
			if err := validate(value.Index(index), fmt.Sprintf("%s[%d]", path, index), depth+1, limits); err != nil {
				return err
			}
		}
	case reflect.Array:
		if value.Len() > maxCollectionItems {
			return fmt.Errorf("%s exceeds collection item budget", path)
		}
		for index := 0; index < value.Len(); index++ {
			if err := validate(value.Index(index), fmt.Sprintf("%s[%d]", path, index), depth+1, limits); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return fmt.Errorf("%s required object is nil", path)
		}
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("%s object keys must be strings", path)
		}
		if value.Len() > maxCollectionItems {
			return fmt.Errorf("%s exceeds collection item budget", path)
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validate(iterator.Value(), path+"."+iterator.Key().String(), depth+1, limits); err != nil {
				return err
			}
		}
	case reflect.Struct:
		typeOf := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := typeOf.Field(index)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			name := field.Name
			if tag := field.Tag.Get("json"); tag != "" {
				for i, character := range tag {
					if character == ',' {
						name = tag[:i]
						break
					}
				}
			}
			if err := validate(value.Field(index), path+"."+name, depth+1, limits); err != nil {
				return err
			}
		}
	}
	return nil
}

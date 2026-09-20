// Package bacon is the repo class manifest store (NERD009 SPEC006): one
// reader and one writer for the BACON manifest, parameterised by location and
// format, holding the document as an ordered map so that every key nsctl does
// not model survives a rewrite.
//
// Forty Python packages write this file. A Go front end that decoded it into
// a struct would drop what the struct does not name, and a front end that
// reorders keys on every write makes a diff of every edit. So the document is
// an Object -- keys in file order, values untyped -- and the verbs edit it by
// key path. repoclass.Manifest and localspec stay what they are: typed,
// read-only views for the code that only needs to read.
package bacon

import (
	"fmt"
	"strings"
)

// Object is a JSON object whose keys keep the order they were read or set in.
// Values are *Object, []any, string, json.Number, bool, nil -- or, when set
// from Go, any value Encode knows how to write.
type Object struct {
	keys   []string
	values map[string]any
}

// NewObject makes an empty object.
func NewObject() *Object {
	return &Object{values: map[string]any{}}
}

// Keys returns the keys in order. The slice is a copy.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Len is the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Get returns a key's value.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.values[key]
	return v, ok
}

// Set writes a key. An existing key keeps its position, which is what makes
// re-running a verb a no-op in the diff rather than a reorder.
func (o *Object) Set(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Delete removes a key, reporting whether it was there.
func (o *Object) Delete(key string) bool {
	if o == nil {
		return false
	}
	if _, ok := o.values[key]; !ok {
		return false
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

// Object returns a nested object, and false when the key is absent or holds
// something else.
func (o *Object) Object(key string) (*Object, bool) {
	v, ok := o.Get(key)
	if !ok {
		return nil, false
	}
	child, ok := v.(*Object)
	return child, ok
}

// EnsureObject returns the nested object at key, creating an empty one when
// the key is absent. A key holding something other than an object is an
// error: silently replacing a user's value is the kind of write this package
// exists to refuse.
func (o *Object) EnsureObject(key string) (*Object, error) {
	v, ok := o.Get(key)
	if !ok {
		child := NewObject()
		o.Set(key, child)
		return child, nil
	}
	child, ok := v.(*Object)
	if !ok {
		return nil, fmt.Errorf("%q is %s, not an object", key, describeType(v))
	}
	return child, nil
}

// Lookup walks a key path.
func (o *Object) Lookup(path ...string) (any, bool) {
	var cur any = o
	for _, key := range path {
		obj, ok := cur.(*Object)
		if !ok {
			return nil, false
		}
		cur, ok = obj.Get(key)
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// SetPath writes a value at a key path, creating intermediate objects.
func (o *Object) SetPath(value any, path ...string) error {
	if len(path) == 0 {
		return fmt.Errorf("empty key path")
	}
	cur := o
	for _, key := range path[:len(path)-1] {
		next, err := cur.EnsureObject(key)
		if err != nil {
			return err
		}
		cur = next
	}
	cur.Set(path[len(path)-1], value)
	return nil
}

// DeletePath removes the key at a path, reporting whether it was there.
// Intermediate objects are left in place, empty or not: a verb that unsets
// one key should not decide that a section is now unwanted.
func (o *Object) DeletePath(path ...string) bool {
	if len(path) == 0 {
		return false
	}
	parent, ok := o.Lookup(path[:len(path)-1]...)
	if !ok {
		return false
	}
	obj, ok := parent.(*Object)
	if !ok {
		return false
	}
	return obj.Delete(path[len(path)-1])
}

// SplitKey turns a dotted key into a path: "deploy.default_configuration.x"
// is three keys. It is the notation `set-config` takes on the command line.
func SplitKey(dotted string) []string {
	return strings.Split(dotted, ".")
}

// JoinKey is SplitKey's inverse, for messages that name the key they wrote.
func JoinKey(path ...string) string {
	return strings.Join(path, ".")
}

// Array returns a key's list value, and false when absent or not a list.
func (o *Object) Array(key string) ([]any, bool) {
	v, ok := o.Get(key)
	if !ok {
		return nil, false
	}
	list, ok := v.([]any)
	return list, ok
}

// String returns a key's string value, and false when absent or not a string.
func (o *Object) String(key string) (string, bool) {
	v, ok := o.Get(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func describeType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case *Object:
		return "an object"
	case []any:
		return "a list"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	default:
		return "a number"
	}
}

package sessionactivity

import (
	"context"
	"reflect"
	"testing"
)

func TestActiveParentsKnownAncestry(t *testing.T) {
	for _, tc := range []struct {
		name         string
		active, live map[string]bool
		parents      map[string]string
		want         map[string]bool
	}{
		{name: "nil inputs", want: map[string]bool{}},
		{name: "missing session", active: map[string]bool{"deleted": true}, want: map[string]bool{}},
		{name: "nested", active: map[string]bool{"grandchild": true}, parents: map[string]string{"grandchild": "child", "child": "parent", "parent": ""}, want: map[string]bool{"child": true, "parent": true}},
		{name: "between turns", live: map[string]bool{"child": true}, parents: map[string]string{"child": "parent"}, want: map[string]bool{"child": true, "parent": true}},
		{name: "cycle", active: map[string]bool{"a": true}, parents: map[string]string{"a": "b", "b": "a"}, want: map[string]bool{"a": true, "b": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ActiveParents(context.Background(), nil, tc.active, tc.parents, tc.live)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parents = %v, want %v", got, tc.want)
			}
		})
	}
}

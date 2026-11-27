package main

import (
	"reflect"
	"testing"
)

func TestExtract(t *testing.T) {
	md := "text\n" +
		"```go\n// comment\npackage main\n\nfunc f() {}\n```\n" +
		"```go\nfunc fragment() {}\n```\n" +
		"```text\npackage main\n```\n" +
		"  ```fincore\n  package p\n  ```\n"
	bs := Extract("spec.md", md)
	if len(bs) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(bs), bs)
	}
	if bs[0].Line != 2 || bs[0].Src != "// comment\npackage main\n\nfunc f() {}\n" {
		t.Errorf("block 0 = %+v", bs[0])
	}
	if bs[1].Line != 14 {
		t.Errorf("block 1 line = %d, want 14", bs[1].Line)
	}
}

func TestDirectives(t *testing.T) {
	src := "// FLAGS: --comptime-fuel=1000\npackage main\n\nvar g Conn // ERROR E3001\n"
	if got := flags(src); !reflect.DeepEqual(got, []string{"--comptime-fuel=1000"}) {
		t.Errorf("flags = %v", got)
	}
	if got := markers(src); !reflect.DeepEqual(got, []string{"4:E3001"}) {
		t.Errorf("markers = %v", got)
	}
}

package gothon

import (
	"context"
	"testing"
)

type addIn struct {
	A int `json:"a"`
	B int `json:"b"`
}

type addOut struct {
	Sum int `json:"sum"`
}

func TestTask(t *testing.T) {
	p, _ := start(t, "basic", 1)
	add := NewTask[addIn, addOut](p, "add")
	out, err := add.Call(context.Background(), addIn{A: 40, B: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Sum != 42 {
		t.Fatalf("sum = %d, want 42", out.Sum)
	}
}

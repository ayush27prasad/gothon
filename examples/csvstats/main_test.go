package main

import (
	"context"
	"testing"

	"github.com/ayush27prasad/gothon"
	"github.com/ayush27prasad/gothon/examples/csvstats/internal/py/stats"
)

func TestDescribe(t *testing.T) {
	rt := gothon.NewRuntime(gothon.Defaults{})
	py, err := stats.Start(context.Background(), rt, stats.Config{Module: gothon.ModuleConfig{Workers: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer py.Close()

	out, err := py.Describe(context.Background(), stats.DescribeIn{CSV: "city,temp\nPune,31.5\nDelhi,38.5\nPune,29\n"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Rows != 3 || len(out.Columns) != 2 {
		t.Fatalf("got %+v", out)
	}
	if city := out.Columns[0]; city.Numeric != nil || city.Top[0] != (stats.Count{Value: "Pune", Count: 2}) {
		t.Fatalf("city: %+v", city)
	}
	if temp := out.Columns[1].Numeric; temp == nil || temp.Max != 38.5 || *temp.Mean != 33 {
		t.Fatalf("temp: %+v", temp)
	}
}

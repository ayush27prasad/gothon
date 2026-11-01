package typed

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ayush27prasad/gothon"
)

func start(t *testing.T) *Client {
	py := os.Getenv("GOTHON_TEST_PYTHON")
	if py == "" {
		py = "python3"
	}
	if exec.Command(py, "-c", "import pydantic").Run() != nil {
		t.Skip("no python with pydantic (set GOTHON_TEST_PYTHON)")
	}
	rt := gothon.NewRuntime(gothon.Defaults{})
	c, err := Start(context.Background(), rt, Config{Module: gothon.ModuleConfig{Python: py, Dir: "../../../testdata/py", Workers: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestGeneratedClient(t *testing.T) {
	c := start(t)
	ctx := context.Background()
	placed := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	red := ColorRed
	qty := 2

	sum, err := c.Summarize(ctx, Order{
		OrderID:  "o-1",
		PlacedAt: placed,
		Items: []Item{
			{Sku: "a", Price: 1.5, Qty: &qty, Color: &red},
			{Sku: "b", Price: 4}, // qty and color keep their Python defaults
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Total != 7 || sum.ItemCount != 2 || len(sum.Colors) != 1 || sum.Colors[0] != ColorRed {
		t.Fatalf("summary = %+v", sum)
	}
	if !sum.PlacedAt.Equal(placed) || sum.FirstItem == nil || sum.FirstItem.Sku != "a" || sum.URL == "" {
		t.Fatalf("summary = %+v", sum)
	}

	if n, err := c.Double(ctx, 21); err != nil || n != 42 {
		t.Fatalf("Double = %d, %v", n, err)
	}
	if names, err := c.Names(ctx); err != nil || len(names) != 2 {
		t.Fatalf("Names = %v, %v", names, err)
	}
	if err := c.Touch(ctx, "o-1"); err != nil {
		t.Fatal(err)
	}
	if v, err := c.EchoAny(ctx, map[string]any{"k": "v"}); err != nil || v.(map[string]any)["k"] != "v" {
		t.Fatalf("EchoAny = %v, %v", v, err)
	}

	var pe *gothon.PythonError
	_, err = c.Summarize(ctx, Order{OrderID: "o-2"}) // items missing
	if !errors.As(err, &pe) || pe.Stage != gothon.StageInput {
		t.Fatalf("want input validation error, got %v", err)
	}
}

// The Tasks interface lets callers swap in a fake.
type fake struct{ Tasks }

func (fake) Double(context.Context, int) (int, error) { return -1, nil }

func TestFake(t *testing.T) {
	var tasks Tasks = fake{}
	if n, _ := tasks.Double(context.Background(), 1); n != -1 {
		t.Fatal("fake not used")
	}
}

package configured

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/ayush27prasad/gothon"
)

func python(t *testing.T) string {
	py := os.Getenv("GOTHON_TEST_PYTHON")
	if py == "" {
		py = "python3"
	}
	if exec.Command(py, "-c", "import pydantic_settings").Run() != nil {
		t.Skip("no python with pydantic-settings (set GOTHON_TEST_PYTHON)")
	}
	return py
}

func TestSettings(t *testing.T) {
	rt := gothon.NewRuntime(gothon.Defaults{})
	cfg := Config{Module: gothon.ModuleConfig{Python: python(t), Dir: "../../../testdata/py", Workers: 1}}
	ctx := context.Background()

	var pe *gothon.PythonError
	_, err := Start(ctx, rt, cfg, Settings{})
	if !errors.As(err, &pe) || pe.Stage != gothon.StageImport || len(pe.Fields) != 1 || pe.Fields[0].Loc[0] != "api_key" {
		t.Fatalf("missing api key: want import-stage validation error, got %v", err)
	}

	retries, debug, region := 5, false, "us"
	c, err := Start(ctx, rt, cfg, Settings{
		APIKey:  "secret",
		Retries: &retries,
		Debug:   &debug, // overrides the Python default of True
		Tags:    []string{"a", "b"},
		Region:  &region,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	info, err := c.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Info{KeyLength: 6, BaseURL: "https://api.example.com", Retries: 5, Debug: false, Tags: []string{"a", "b"}, Region: "us"}
	if info.KeyLength != want.KeyLength || info.BaseURL != want.BaseURL || info.Retries != want.Retries ||
		info.Debug != want.Debug || len(info.Tags) != 2 || info.Region != want.Region {
		t.Fatalf("info = %+v, want %+v", info, want)
	}
}

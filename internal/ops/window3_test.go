package ops

import (
	"context"
	"testing"
)

// The board of SimulateBoardWith has the values and the files asked for
func TestSimulateBoardWith(t *testing.T) {
	end := SimulateBoardWith(SimOptions{
		NVS:   []NVSEntry{{Namespace: "app", Key: "count", Type: 0x04, Value: uint64(7)}},
		Files: map[string][]byte{"www/index.html": []byte("<h1>hi</h1>"), "cfg.json": []byte("{}")},
	})
	defer end()
	p, err := PickPort("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := Connect(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx, false)
	im, err := s.ReadNVS(ctx, "nvs", "")
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := im.Get("app", "count"); !ok || e.Value != uint64(7) {
		t.Fatalf("nvs %+v", im.Entries)
	}
	fs, err := s.ReadFS(ctx, "storage", nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := fs.ReadFile("www/index.html"); err != nil || string(data) != "<h1>hi</h1>" {
		t.Fatalf("%q %v", data, err)
	}
}

// A field of the simulated eFuse model is planned, refused, and burned
func TestSimulatedEFuse(t *testing.T) {
	end := SimulateBoardWith(SimOptions{EFuse: true})
	defer end()
	p, _ := PickPort("")
	ctx := context.Background()
	s, err := Connect(ctx, p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx, false)
	d, err := s.ReadEFuse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Values) == 0 {
		t.Fatal("no fields")
	}
	if _, err := s.PlanEFuse(d, []EFuseChange{EFuseUint("NO_SUCH_FIELD", 1)}); err == nil {
		t.Fatal("an unknown field is planned")
	} else if r, ok := EFuseRefusal(err); !ok || len(r) == 0 {
		t.Fatalf("not a refusal: %v", err)
	}
	plan, err := s.PlanEFuse(d, []EFuseChange{EFuseUint("USER_DATA", 0xA5)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BurnEFuse(ctx, plan, "burn"); err == nil {
		t.Fatal("burned without the word")
	}
	if _, err := s.BurnEFuse(ctx, plan, BurnWord); err != nil {
		t.Fatal(err)
	}
	d, _ = s.ReadEFuse(ctx)
	if v, _ := d.Value("USER_DATA"); v.Raw[0] != 0xA5 {
		t.Fatalf("after the burn %v", v.Raw)
	}
}

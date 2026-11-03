package config

import "testing"

func TestOperationsLog(t *testing.T) {
	SetDir(t.TempDir())
	if ops, err := Operations(); err != nil || len(ops) != 0 {
		t.Fatal(ops, err)
	}
	LogOperation("7C:DF:A1:00:12:34", "ttyUSB0", "Erased the partition nvs")
	LogOperation("", "", "Wrote the\npartition table")
	ops, err := Operations()
	if err != nil || len(ops) != 2 {
		t.Fatal(ops, err)
	}
	if ops[0].MAC != "7C:DF:A1:00:12:34" || ops[1].What != "Wrote the partition table" || ops[1].MAC != "-" {
		t.Fatalf("%+v", ops)
	}
}

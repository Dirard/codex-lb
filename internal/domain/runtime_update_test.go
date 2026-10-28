package domain

import "testing"

func TestRuntimeDescriptorSwitchRules(t *testing.T) {
	t.Parallel()
	base := RuntimeDescriptor{Version: "go-v1.2.0", GOOS: "linux", GOARCH: "amd64", SchemaVersion: 41, UpdateProtocol: 1, Handoff: 1, SchemaFloor: 40}
	with := func(change func(*RuntimeDescriptor)) RuntimeDescriptor { d := base; change(&d); return d }
	legacy := with(func(d *RuntimeDescriptor) { d.Version, d.SchemaVersion, d.Handoff, d.SchemaFloor = "go-v1.1.1", 40, 0, 0 })
	additive := with(func(d *RuntimeDescriptor) { d.Version, d.SchemaVersion = "go-v1.3.0", 42 })
	breaking := with(func(d *RuntimeDescriptor) { d.Version, d.SchemaVersion, d.SchemaFloor = "go-v1.4.0", 43, 43 })
	cases := []struct {
		name             string
		current, target  RuntimeDescriptor
		shared, seamless bool
	}{
		{"same schema", base, with(func(d *RuntimeDescriptor) { d.Version = "go-v1.2.1" }), true, true},
		{"additive migration", base, additive, true, true},
		{"additive rollback", additive, base, true, true},
		{"breaking migration", base, breaking, false, false},
		{"legacy running worker", legacy, base, false, false},
		{"rollback to legacy worker", base, legacy, false, false},
		{"other platform", base, with(func(d *RuntimeDescriptor) { d.GOARCH = "arm64" }), true, false},
		{"other update protocol", base, with(func(d *RuntimeDescriptor) { d.UpdateProtocol = 2 }), true, false},
		{"floor above schema is ignored", base, with(func(d *RuntimeDescriptor) { d.SchemaVersion, d.SchemaFloor = 42, 99 }), false, false},
	}
	for _, tc := range cases {
		if got := SharedSchema(tc.current, tc.target); got != tc.shared {
			t.Errorf("%s: SharedSchema = %v, want %v", tc.name, got, tc.shared)
		}
		if got := SeamlessSwitch(tc.current, tc.target); got != tc.seamless {
			t.Errorf("%s: SeamlessSwitch = %v, want %v", tc.name, got, tc.seamless)
		}
	}
	if base.Identity() != with(func(d *RuntimeDescriptor) { d.Handoff, d.SchemaFloor = 0, 0 }) {
		t.Fatal("Identity must drop capability fields only")
	}
}

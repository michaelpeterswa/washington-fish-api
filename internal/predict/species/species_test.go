package species

import "testing"

func TestLookup(t *testing.T) {
	cases := []struct {
		name  string
		want  string
		group Group
	}{
		{"Rainbow", "Rainbow Trout", Coldwater},
		{"Largemouth Bass", "Largemouth Bass", Warmwater},
		{"Smallmouth Bass", "Smallmouth Bass", Warmwater},
		{"Walleye", "Walleye", Coolwater},
		{"Yellow Perch", "Yellow Perch", Coolwater},
		{"Kokanee", "Kokanee", Coldwater},
		{"Coastal Cutthroat", "Cutthroat Trout", Coldwater},
		{"Tiger Muskie", "Tiger Muskie", Coolwater}, // must beat generic "tiger"
		{"Tiger", "Tiger Trout", Coldwater},
		{"Bluegill", "Bluegill", Warmwater},
		{"", "Trout", Coldwater},         // empty -> default
		{"Sturgeon", "Trout", Coldwater}, // unknown -> default
	}
	for _, c := range cases {
		got := Lookup(c.name)
		if got.Canonical != c.want || got.Group != c.group {
			t.Errorf("Lookup(%q) = {%s, %s}, want {%s, %s}", c.name, got.Canonical, got.Group, c.want, c.group)
		}
	}
}

func TestWindowsOrdered(t *testing.T) {
	for _, e := range catalog {
		s := e.sp
		if !(s.TolLo <= s.OptLo && s.OptLo < s.OptHi && s.OptHi <= s.TolHi) {
			t.Errorf("%s: window not ordered TolLo<=OptLo<OptHi<=TolHi: %+v", s.Canonical, s)
		}
	}
}

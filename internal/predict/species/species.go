// Package species is a small knowledge base mapping WDFW species names to a
// thermal-preference profile (cold/cool/warm water + an optimal temperature
// window). It's the basis for the species-aware water-temperature bite factor:
// the same lake at the same water temp is prime for bass and poor for trout.
package species

import "strings"

// Group is the broad thermal guild.
type Group string

const (
	Coldwater Group = "coldwater"
	Coolwater Group = "coolwater"
	Warmwater Group = "warmwater"
)

// Species is a canonical species with its optimal + tolerable water-temp bands
// (degrees C). Optimal = actively feeding; outside Tolerable = stressed/slow.
type Species struct {
	Canonical string
	Group     Group
	OptLo     float64
	OptHi     float64
	TolLo     float64
	TolHi     float64
}

// entry pairs match keywords with a species profile. Order matters: more
// specific keywords (e.g. "tiger muskie") come before generic ones ("tiger").
type entry struct {
	keywords []string
	sp       Species
}

var catalog = []entry{
	{[]string{"kokanee"}, Species{"Kokanee", Coldwater, 10, 15, 4, 18}},
	{[]string{"largemouth"}, Species{"Largemouth Bass", Warmwater, 20, 27, 15, 32}},
	{[]string{"smallmouth"}, Species{"Smallmouth Bass", Warmwater, 18, 24, 13, 29}},
	{[]string{"tiger muskie", "muskie", "musky"}, Species{"Tiger Muskie", Coolwater, 18, 24, 12, 27}},
	{[]string{"walleye"}, Species{"Walleye", Coolwater, 16, 22, 10, 27}},
	{[]string{"yellow perch", "perch"}, Species{"Yellow Perch", Coolwater, 18, 24, 12, 28}},
	{[]string{"crappie"}, Species{"Black Crappie", Warmwater, 20, 26, 14, 30}},
	{[]string{"bluegill", "pumpkinseed", "sunfish"}, Species{"Bluegill", Warmwater, 22, 28, 16, 32}},
	{[]string{"bullhead", "catfish"}, Species{"Brown Bullhead", Warmwater, 24, 30, 18, 33}},
	// Trout guild (coldwater). Generic "bass" left out on purpose — always qualified.
	{[]string{"cutthroat"}, Species{"Cutthroat Trout", Coldwater, 9, 16, 4, 21}},
	{[]string{"brook"}, Species{"Brook Trout", Coldwater, 11, 16, 4, 20}},
	{[]string{"brown trout", "brown"}, Species{"Brown Trout", Coldwater, 12, 19, 5, 23}},
	{[]string{"rainbow", "steelhead"}, Species{"Rainbow Trout", Coldwater, 10, 18, 4, 22}},
	{[]string{"tiger trout", "tiger"}, Species{"Tiger Trout", Coldwater, 11, 18, 4, 21}},
	{[]string{"golden"}, Species{"Golden Trout", Coldwater, 8, 16, 3, 20}},
}

// defaultSpecies is used when a name doesn't match — a generic stocked trout,
// since the launch scope is trout-first.
var defaultSpecies = Species{"Trout", Coldwater, 10, 18, 4, 22}

// Lookup returns the thermal profile for a species name (case/space-insensitive
// keyword match), falling back to a generic trout profile.
func Lookup(name string) Species {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return defaultSpecies
	}
	for _, e := range catalog {
		for _, kw := range e.keywords {
			if strings.Contains(n, kw) {
				return e.sp
			}
		}
	}
	return defaultSpecies
}

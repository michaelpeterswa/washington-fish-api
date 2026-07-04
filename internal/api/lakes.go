package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/michaelpeterswa/washington-fish-api/internal/predict/bite"
	"github.com/michaelpeterswa/washington-fish-api/internal/store"
	"github.com/michaelpeterswa/washington-fish-api/internal/store/db"
)

// resolveUnit reads ?units= (falling back to the server default).
func (s *Server) resolveUnit(r *http.Request) bite.TempUnit {
	if u := r.URL.Query().Get("units"); u != "" {
		return bite.ParseTempUnit(u)
	}
	return s.DefaultUnit
}

func unitLabel(u bite.TempUnit) string {
	if u == bite.Fahrenheit {
		return "F"
	}
	return "C"
}

// convTemp converts a nullable Celsius value to the unit, rounded to 1 decimal.
func convTemp(c *float64, u bite.TempUnit) *float64 {
	if c == nil {
		return nil
	}
	v := float64(int(bite.ConvertTemp(*c, u)*10+0.5)) / 10
	return &v
}

// ---- DTOs ----

type lakeDTO struct {
	ID         int64    `json:"id"`
	GeoCode    *string  `json:"geo_code"`
	Name       string   `json:"name"`
	County     *string  `json:"county"`
	LakeType   *string  `json:"lake_type"`
	Lat        *float64 `json:"lat"`
	Lon        *float64 `json:"lon"`
	AreaM2     *float64 `json:"area_m2"`
	ElevM      *float64 `json:"elev_m"`
	DepthMaxM  *float64 `json:"depth_max_m"`
	DepthMeanM *float64 `json:"depth_mean_m"`
	DistanceKm *float64 `json:"distance_km,omitempty"`
}

func toLakeDTO(l store.LakeSummary) lakeDTO {
	return lakeDTO{
		ID: l.ID, GeoCode: l.GeoCode, Name: l.Name, County: l.County, LakeType: l.LakeType,
		Lat: l.Lat, Lon: l.Lon, AreaM2: l.AreaM2, ElevM: l.ElevM,
		DepthMaxM: l.DepthMaxM, DepthMeanM: l.DepthMeanM, DistanceKm: roundKm(l.DistanceKm),
	}
}

type factorDTO struct {
	Name         string  `json:"name"`
	Contribution float64 `json:"contribution"`
	Reason       string  `json:"reason"`
}

type speciesDTO struct {
	Species    string  `json:"species"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	UpdatedAt  string  `json:"updated_at"`
}

func toSpeciesDTOs(rows []db.SpeciesForLakeRow) []speciesDTO {
	out := make([]speciesDTO, len(rows))
	for i, r := range rows {
		out[i] = speciesDTO{
			Species: r.Species, Confidence: r.Confidence, Source: r.Source,
			UpdatedAt: r.UpdatedAt.Time.Format(time.RFC3339),
		}
	}
	return out
}

// ---- handlers ----

func (s *Server) handleSearchLakes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := store.LakeSearch{
		Name:     q.Get("q"),
		County:   q.Get("county"),
		Species:  q.Get("species"),
		LakeType: q.Get("lake_type"),
		Limit:    atoiDefault(q.Get("limit"), 100),
	}
	if lat, lon, ok := parseLatLon(q); ok {
		search.Lat, search.Lon = &lat, &lon
		search.RadiusKm = atofDefault(q.Get("radius_km"), 0)
	}

	lakes, err := s.Store.QueryLakes(r.Context(), search)
	if err != nil {
		s.serverError(w, r, "search lakes", err)
		return
	}
	dtos := make([]lakeDTO, len(lakes))
	for i, l := range lakes {
		dtos[i] = toLakeDTO(l)
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(dtos), "lakes": dtos})
}

func (s *Server) handleLakeDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := lakeID(w, r)
	if !ok {
		return
	}
	lake, err := s.Store.LakeByID(r.Context(), id)
	if errors.Is(err, store.ErrLakeNotFound) {
		writeError(w, http.StatusNotFound, "lake not found")
		return
	}
	if err != nil {
		s.serverError(w, r, "lake detail", err)
		return
	}
	species, err := s.Store.Q.SpeciesForLake(r.Context(), id)
	if err != nil {
		s.serverError(w, r, "lake species", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lake":    toLakeDTO(*lake),
		"species": toSpeciesDTOs(species),
	})
}

func (s *Server) handleLakeSpecies(w http.ResponseWriter, r *http.Request) {
	id, ok := lakeID(w, r)
	if !ok {
		return
	}
	species, err := s.Store.Q.SpeciesForLake(r.Context(), id)
	if err != nil {
		s.serverError(w, r, "lake species", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lake_id": id, "species": toSpeciesDTOs(species)})
}

func (s *Server) handleLakePrediction(w http.ResponseWriter, r *http.Request) {
	id, ok := lakeID(w, r)
	if !ok {
		return
	}
	unit := s.resolveUnit(r)
	p, err := s.Predict.LakePrediction(r.Context(), id, r.URL.Query().Get("species"), unit)
	if errors.Is(err, store.ErrLakeNotFound) {
		writeError(w, http.StatusNotFound, "lake not found")
		return
	}
	if err != nil {
		s.serverError(w, r, "lake prediction", err)
		return
	}

	resp := map[string]any{
		"lake_id":                    id,
		"target_species":             p.TargetSpecies,
		"temp_unit":                  unitLabel(unit),
		"days_since_catchable_plant": p.DaysSinceCatchablePlant,
		"last_plant_species":         p.LastPlantSpecies,
	}
	if p.Nowcast != nil {
		factors := make([]factorDTO, len(p.Nowcast.Factors))
		for i, f := range p.Nowcast.Factors {
			factors[i] = factorDTO{f.Name, f.Contribution, f.Reason}
		}
		resp["nowcast"] = map[string]any{
			"valid_at":   p.Nowcast.ValidAt.Format(time.RFC3339),
			"score":      p.Nowcast.Score,
			"confidence": round3(p.Nowcast.Confidence),
			"water_temp": convTemp(p.Nowcast.WaterTempC, unit),
			"factors":    factors,
		}
	}
	fc := make([]map[string]any, len(p.Forecast))
	for i, f := range p.Forecast {
		fc[i] = map[string]any{
			"valid_at": f.ValidAt.Format(time.RFC3339), "horizon_h": f.HorizonH,
			"score": f.Score, "confidence": round3(f.Confidence),
		}
	}
	resp["forecast"] = fc
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRank(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, lon, ok := parseLatLon(q)
	if !ok {
		writeError(w, http.StatusBadRequest, "lat and lon query params are required")
		return
	}
	params := store.RankParams{
		Lat: lat, Lon: lon,
		RadiusKm: atofDefault(q.Get("radius_km"), 80),
		Species:  q.Get("species"),
		Limit:    atoiDefault(q.Get("limit"), 20),
	}

	ranked, err := s.Predict.RankLakes(r.Context(), params, s.resolveUnit(r))
	if err != nil {
		s.serverError(w, r, "rank", err)
		return
	}
	results := make([]map[string]any, len(ranked))
	for i, rk := range ranked {
		results[i] = map[string]any{
			"lake":       toLakeDTO(rk.Lake),
			"score":      rk.Score,
			"confidence": round3(rk.Confidence),
			"why":        rk.TopReason,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"origin":    map[string]float64{"lat": lat, "lon": lon},
		"radius_km": params.RadiusKm,
		"count":     len(results),
		"results":   results,
	})
}

// ---- helpers ----

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.Logger.ErrorContext(r.Context(), "handler error", slog.String("op", op), slog.String("error", err.Error()))
	writeError(w, http.StatusInternalServerError, "internal error")
}

func lakeID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid lake id")
		return 0, false
	}
	return id, true
}

func parseLatLon(q map[string][]string) (lat, lon float64, ok bool) {
	latStr, lonStr := first(q["lat"]), first(q["lon"])
	if latStr == "" || lonStr == "" {
		return 0, 0, false
	}
	la, err1 := strconv.ParseFloat(latStr, 64)
	lo, err2 := strconv.ParseFloat(lonStr, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return la, lo, true
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func atofDefault(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return def
}

func roundKm(km *float64) *float64 {
	if km == nil {
		return nil
	}
	v := float64(int(*km*100+0.5)) / 100
	return &v
}

func round3(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
}

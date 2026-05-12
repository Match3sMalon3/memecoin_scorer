package outcomes

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

func RegisterHandlers(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/outcomes/summary", summaryHandler(db))
	mux.HandleFunc("/api/outcomes/pending", pendingHandler(db))
}

func summaryHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if db == nil {
			_, _ = w.Write([]byte("[]\n"))
			return
		}
		const q = `
			SELECT classification_version, setup_mode, action,
			       COALESCE(liq_source,'') AS liq_source,
			       blocker_signature, checkpoint_s,
			       n_completed, n_tradeable, n_1_2x_clean, n_2x_clean, n_rugged,
			       COALESCE(avg_return,0), COALESCE(avg_max_return,0),
			       COALESCE(median_return,0), COALESCE(median_max_return,0)
			FROM v_outcomes_summary
			ORDER BY classification_version, setup_mode, action, liq_source, checkpoint_s
		`
		rows, err := db.QueryContext(r.Context(), q)
		if err != nil {
			_, _ = w.Write([]byte("[]\n"))
			return
		}
		defer rows.Close()
		type rec struct {
			ClassificationVersion string  `json:"classification_version"`
			SetupMode             string  `json:"setup_mode"`
			Action                string  `json:"action"`
			LiqSource             string  `json:"liq_source"`
			BlockerSignature      string  `json:"blocker_signature"`
			CheckpointS           int     `json:"checkpoint_s"`
			NCompleted            int     `json:"n_completed"`
			NTradeable            int     `json:"n_tradeable"`
			N1_2xClean            int     `json:"n_1_2x_clean"`
			N2xClean              int     `json:"n_2x_clean"`
			NRugged               int     `json:"n_rugged"`
			AvgReturn             float64 `json:"avg_return"`
			AvgMaxReturn          float64 `json:"avg_max_return"`
			MedianReturn          float64 `json:"median_return"`
			MedianMaxReturn       float64 `json:"median_max_return"`
		}
		out := []rec{}
		for rows.Next() {
			var x rec
			if err := rows.Scan(&x.ClassificationVersion, &x.SetupMode, &x.Action, &x.LiqSource,
				&x.BlockerSignature, &x.CheckpointS, &x.NCompleted, &x.NTradeable,
				&x.N1_2xClean, &x.N2xClean, &x.NRugged,
				&x.AvgReturn, &x.AvgMaxReturn, &x.MedianReturn, &x.MedianMaxReturn); err != nil {
				_, _ = w.Write([]byte("[]\n"))
				return
			}
			out = append(out, x)
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}

func pendingHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if db == nil {
			_, _ = w.Write([]byte("[]\n"))
			return
		}
		rows, err := db.QueryContext(r.Context(),
			`SELECT classification_version, setup_mode, n_pending FROM v_outcomes_pending`)
		if err != nil {
			_, _ = w.Write([]byte("[]\n"))
			return
		}
		defer rows.Close()
		type rec struct {
			ClassificationVersion string `json:"classification_version"`
			SetupMode             string `json:"setup_mode"`
			NPending              int    `json:"n_pending"`
		}
		out := []rec{}
		for rows.Next() {
			var x rec
			if err := rows.Scan(&x.ClassificationVersion, &x.SetupMode, &x.NPending); err != nil {
				_, _ = w.Write([]byte("[]\n"))
				return
			}
			out = append(out, x)
		}
		_ = json.NewEncoder(w).Encode(out)
	}
}

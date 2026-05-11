package alerts

import (
	"sync"
	"time"
)

type Alert struct {
	Mint                   string    `json:"mint"`
	Symbol                 string    `json:"symbol"`
	Score                  float64   `json:"score"`
	AgeMinutes             int       `json:"age_minutes"`
	Reasons                []string  `json:"reasons"`
	RiskFlags              []string  `json:"risk_flags"`
	Invalidation           []string  `json:"invalidation"`
	GMGNUrl                string    `json:"gmgn_url"`
	SolscanUrl             string    `json:"solscan_url"`
	RealPoolDepth          float64   `json:"real_pool_depth_sol"`
	LiqSource              string    `json:"liquidity_evidence_source"`
	DeployerRugRate        float64   `json:"deployer_rug_rate"`
	DeployerLaunches       int       `json:"deployer_launches"`
	FiredAt                time.Time `json:"fired_at"`
	HistoricalPrecisionPct float64   `json:"historical_precision_pct"`
	LiveSignalsTotal       int       `json:"live_signals_total"`
}

var broker = struct {
	sync.Mutex
	subs map[<-chan Alert]chan Alert
}{subs: map[<-chan Alert]chan Alert{}}

func Publish(a Alert) {
	broker.Lock()
	defer broker.Unlock()
	for _, ch := range broker.subs {
		select {
		case ch <- a:
		default:
		}
	}
}

func Subscribe() <-chan Alert {
	ch := make(chan Alert, 8)
	broker.Lock()
	broker.subs[(<-chan Alert)(ch)] = ch
	broker.Unlock()
	return ch
}

func Unsubscribe(sub <-chan Alert) {
	broker.Lock()
	if ch, ok := broker.subs[sub]; ok {
		delete(broker.subs, sub)
		close(ch)
	}
	broker.Unlock()
}

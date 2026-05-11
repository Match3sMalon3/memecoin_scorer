package devprint

import (
	"context"
	"database/sql"
	"os"

	_ "github.com/lib/pq"
)

type DeployerProfile struct {
	Address       string
	TotalLaunches int
	RugCount      int
	RugRate       float64
	IsVetoed      bool
	VetoReason    string
}

func openDB() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://localhost:5432/meme_trading_system_v1?sslmode=disable"
	}
	return sql.Open("postgres", dsn)
}

func GetProfile(address string) (DeployerProfile, error) {
	p := DeployerProfile{Address: address}
	if address == "" {
		return p, nil
	}
	db, err := openDB()
	if err != nil {
		return p, err
	}
	defer db.Close()
	err = db.QueryRowContext(context.Background(), `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE outcome = 'miss')
		FROM deployer_history WHERE deployer_address = $1
	`, address).Scan(&p.TotalLaunches, &p.RugCount)
	if err != nil {
		return p, err
	}
	if p.TotalLaunches > 0 {
		p.RugRate = float64(p.RugCount) / float64(p.TotalLaunches)
	}
	p.IsVetoed = p.RugRate >= 0.60 && p.TotalLaunches >= 3
	if p.IsVetoed {
		p.VetoReason = "serial rugger"
	}
	return p, nil
}

func IsVetoed(address string) bool {
	p, err := GetProfile(address)
	return err == nil && p.IsVetoed
}

func RecordLaunch(deployer, mint string) error {
	if deployer == "" || mint == "" {
		return nil
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO deployer_history (deployer_address, mint)
		VALUES ($1,$2)
		ON CONFLICT (deployer_address, mint) DO NOTHING
	`, deployer, mint)
	return err
}

func UpdateDeployerOutcome(mint, outcome string) error {
	db, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(context.Background(), `UPDATE deployer_history SET outcome=$2 WHERE mint=$1`, mint, outcome)
	return err
}

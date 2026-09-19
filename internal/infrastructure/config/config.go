package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
)

type Config struct {
	RunAddress           string
	DatabaseURI          string
	AccrualSystemAddress string
}

func Load(args []string) (Config, error) {
	var cfg Config
	flags := flag.NewFlagSet("gophermart", flag.ContinueOnError)
	flags.StringVar(&cfg.RunAddress, "a", "localhost:8080", "HTTP listen address (RUN_ADDRESS)")
	flags.StringVar(&cfg.DatabaseURI, "d", "", "reserved PostgreSQL URI (DATABASE_URI); adapter not implemented")
	flags.StringVar(&cfg.AccrualSystemAddress, "r", "", "reserved accrual URL (ACCRUAL_SYSTEM_ADDRESS); adapter not implemented")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments")
	}
	if value := os.Getenv("RUN_ADDRESS"); value != "" {
		cfg.RunAddress = value
	}
	if value := os.Getenv("DATABASE_URI"); value != "" {
		cfg.DatabaseURI = value
	}
	if value := os.Getenv("ACCRUAL_SYSTEM_ADDRESS"); value != "" {
		cfg.AccrualSystemAddress = value
	}

	_, port, err := net.SplitHostPort(cfg.RunAddress)
	if err != nil {
		return Config{}, fmt.Errorf("invalid RUN_ADDRESS/-a: expected host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return Config{}, fmt.Errorf("invalid RUN_ADDRESS/-a: port must be between 0 and 65535")
	}
	return cfg, nil
}

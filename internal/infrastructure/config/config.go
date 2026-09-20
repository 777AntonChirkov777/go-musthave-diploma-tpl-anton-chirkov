package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	defaultRunAddress = "localhost:8080"
	defaultConfigPath = "config.yaml"
)

type Config struct {
	RunAddress           string `yaml:"run_address"`
	DatabaseURI          string `yaml:"database_uri"`
	AccrualSystemAddress string `yaml:"accrual_system_address"`
}

// Load reads defaults, YAML, explicit flags, and non-empty environment variables,
// in increasing order of priority. The default YAML file is optional.
func Load(args []string) (Config, error) {
	var flagConfig Config
	var configPath string
	flags := flag.NewFlagSet("gophermart", flag.ContinueOnError)
	flags.StringVar(&flagConfig.RunAddress, "a", defaultRunAddress, "HTTP listen address (RUN_ADDRESS)")
	flags.StringVar(&flagConfig.DatabaseURI, "d", "", "required PostgreSQL URI (DATABASE_URI)")
	flags.StringVar(&flagConfig.AccrualSystemAddress, "r", "", "reserved accrual URL (ACCRUAL_SYSTEM_ADDRESS); adapter not implemented")
	flags.StringVar(&configPath, "c", defaultConfigPath, "YAML configuration file")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments")
	}

	explicitFlags := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})
	cfg := Config{RunAddress: defaultRunAddress}
	if err := loadFile(configPath, explicitFlags["c"], &cfg); err != nil {
		return Config{}, err
	}
	if explicitFlags["a"] {
		cfg.RunAddress = flagConfig.RunAddress
	}
	if explicitFlags["d"] {
		cfg.DatabaseURI = flagConfig.DatabaseURI
	}
	if explicitFlags["r"] {
		cfg.AccrualSystemAddress = flagConfig.AccrualSystemAddress
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
	if strings.TrimSpace(cfg.DatabaseURI) == "" {
		return Config{}, fmt.Errorf("invalid DATABASE_URI/-d/database_uri: PostgreSQL URI is required")
	}

	_, port, err := net.SplitHostPort(cfg.RunAddress)
	if err != nil {
		return Config{}, fmt.Errorf("invalid RUN_ADDRESS/-a/run_address: expected host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return Config{}, fmt.Errorf("invalid RUN_ADDRESS/-a/run_address: port must be between 0 and 65535")
	}
	return cfg, nil
}

func loadFile(path string, required bool, cfg *Config) error {
	file, err := os.Open(path)
	if err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open config file %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode config file %q: %w", path, err)
		}
		return fmt.Errorf("config file %q must contain a single YAML document", path)
	}
	return nil
}

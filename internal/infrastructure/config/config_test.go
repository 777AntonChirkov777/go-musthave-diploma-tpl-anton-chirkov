package config_test

import (
	"errors"
	"flag"
	"os"
	"testing"

	"diplom/internal/infrastructure/config"
)

func TestLoadSources(t *testing.T) {
	const yamlConfig = "run_address: 'file.local:9000'\ndatabase_uri: 'postgres://file/db'\naccrual_system_address: 'http://file.local:9001'\n"
	defaults := config.Config{RunAddress: "localhost:8080"}
	fromFile := config.Config{
		RunAddress:           "file.local:9000",
		DatabaseURI:          "postgres://file/db",
		AccrualSystemAddress: "http://file.local:9001",
	}
	tests := []struct {
		name  string
		files map[string]string
		args  []string
		env   map[string]string
		want  config.Config
	}{
		{
			name: "defaults without optional file",
			want: defaults,
		},
		{
			name:  "snake case YAML fields",
			files: map[string]string{"config.yaml": yamlConfig},
			want:  fromFile,
		},
		{
			name:  "partial file preserves defaults",
			files: map[string]string{"config.yaml": "database_uri: 'postgres://file/db'\n"},
			want:  config.Config{RunAddress: "localhost:8080", DatabaseURI: "postgres://file/db"},
		},
		{
			name: "flags without file",
			args: []string{"-a", "flag.local:9100", "-d", "postgres://flag/db", "-r", "http://flag.local:9101"},
			want: config.Config{RunAddress: "flag.local:9100", DatabaseURI: "postgres://flag/db", AccrualSystemAddress: "http://flag.local:9101"},
		},
		{
			name:  "explicit flags override file",
			files: map[string]string{"config.yaml": yamlConfig},
			args:  []string{"-a", "flag.local:9100", "-d", "postgres://flag/db", "-r", "http://flag.local:9101"},
			want:  config.Config{RunAddress: "flag.local:9100", DatabaseURI: "postgres://flag/db", AccrualSystemAddress: "http://flag.local:9101"},
		},
		{
			name:  "environment overrides flags and file",
			files: map[string]string{"config.yaml": yamlConfig},
			args:  []string{"-a", "flag.local:9100", "-d", "postgres://flag/db", "-r", "http://flag.local:9101"},
			env: map[string]string{
				"RUN_ADDRESS":            "env.local:9200",
				"DATABASE_URI":           "postgres://env/db",
				"ACCRUAL_SYSTEM_ADDRESS": "http://env.local:9201",
			},
			want: config.Config{RunAddress: "env.local:9200", DatabaseURI: "postgres://env/db", AccrualSystemAddress: "http://env.local:9201"},
		},
		{
			name:  "each field resolves its own source",
			files: map[string]string{"config.yaml": yamlConfig},
			args:  []string{"-d", "postgres://flag/db"},
			env:   map[string]string{"RUN_ADDRESS": "env.local:9200"},
			want:  config.Config{RunAddress: "env.local:9200", DatabaseURI: "postgres://flag/db", AccrualSystemAddress: "http://file.local:9001"},
		},
		{
			name:  "empty explicit flags clear file values",
			files: map[string]string{"config.yaml": yamlConfig},
			args:  []string{"-d=", "-r="},
			want:  config.Config{RunAddress: "file.local:9000"},
		},
		{
			name:  "empty environment falls back to flags",
			files: map[string]string{"config.yaml": yamlConfig},
			args:  []string{"-a", "flag.local:9100", "-d", "postgres://flag/db", "-r", "http://flag.local:9101"},
			env: map[string]string{
				"RUN_ADDRESS":            "",
				"DATABASE_URI":           "",
				"ACCRUAL_SYSTEM_ADDRESS": "",
			},
			want: config.Config{RunAddress: "flag.local:9100", DatabaseURI: "postgres://flag/db", AccrualSystemAddress: "http://flag.local:9101"},
		},
		{
			name: "explicit config path replaces default path",
			files: map[string]string{
				"config.yaml": "run_address: [\n",
				"custom.yaml": yamlConfig,
			},
			args: []string{"-c", "custom.yaml"},
			want: fromFile,
		},
		{
			name:  "empty file keeps defaults",
			files: map[string]string{"config.yaml": ""},
			want:  defaults,
		},
		{
			name:  "comment only file keeps defaults",
			files: map[string]string{"config.yaml": "# Local configuration\n\n"},
			want:  defaults,
		},
		{
			name:  "null fields keep defaults",
			files: map[string]string{"config.yaml": "run_address: null\ndatabase_uri: ~\naccrual_system_address:\n"},
			want:  defaults,
		},
		{
			name:  "null document keeps defaults",
			files: map[string]string{"config.yaml": "null\n"},
			want:  defaults,
		},
		{
			name:  "address validation follows flag override",
			files: map[string]string{"config.yaml": "run_address: invalid\n"},
			args:  []string{"-a", "flag.local:9100"},
			want:  config.Config{RunAddress: "flag.local:9100"},
		},
		{
			name:  "address validation follows environment override",
			files: map[string]string{"config.yaml": "run_address: invalid\n"},
			args:  []string{"-a", "also-invalid"},
			env:   map[string]string{"RUN_ADDRESS": "env.local:9200"},
			want:  config.Config{RunAddress: "env.local:9200"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepareConfig(t, tt.files)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			got, err := config.Load(tt.args)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		args  []string
	}{
		{name: "missing explicit config", args: []string{"-c", "missing.yaml"}},
		{name: "missing explicitly selected default config", args: []string{"-c", "config.yaml"}},
		{name: "unknown flag", args: []string{"-unknown"}},
		{name: "positional argument", args: []string{"unexpected"}},
		{name: "positional argument after flags", args: []string{"-a", "localhost:8080", "unexpected"}},
		{name: "missing flag value", args: []string{"-a"}},
		{name: "malformed YAML", files: map[string]string{"config.yaml": "run_address: [\n"}},
		{name: "unknown YAML key", files: map[string]string{"config.yaml": "run_adress: 'localhost:8080'\n"}},
		{name: "duplicate YAML key", files: map[string]string{"config.yaml": "run_address: 'localhost:8080'\nrun_address: 'localhost:9000'\n"}},
		{name: "multiple YAML documents", files: map[string]string{"config.yaml": "run_address: 'localhost:8080'\n---\ndatabase_uri: 'postgres://file/db'\n"}},
		{name: "invalid file address", files: map[string]string{"config.yaml": "run_address: invalid\n"}},
		{name: "empty explicit address overrides file", files: map[string]string{"config.yaml": "run_address: 'localhost:8080'\n"}, args: []string{"-a="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepareConfig(t, tt.files)
			if _, err := config.Load(tt.args); err == nil {
				t.Fatal("Load() succeeded, want an error")
			}
		})
	}
}

func TestLoadHelpDoesNotReadConfig(t *testing.T) {
	prepareConfig(t, map[string]string{"config.yaml": "run_address: [\n"})
	for _, args := range [][]string{{"-h"}, {"-c", "missing.yaml", "-h"}} {
		if _, err := config.Load(args); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("Load(%q) error = %v, want flag.ErrHelp", args, err)
		}
	}
}

func TestLoadRunAddress(t *testing.T) {
	tests := []struct {
		address string
		valid   bool
	}{
		{address: "localhost:8080", valid: true},
		{address: "127.0.0.1:1", valid: true},
		{address: ":0", valid: true},
		{address: "[::1]:65535", valid: true},
		{address: "[::]:8080", valid: true},
		{address: "localhost"},
		{address: "localhost:"},
		{address: "localhost:http"},
		{address: "localhost:-1"},
		{address: "localhost:65536"},
		{address: "localhost:999999999999999999999999"},
		{address: "localhost:80.5"},
		{address: "::1:8080"},
		{address: "http://localhost:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			prepareConfig(t, nil)
			t.Setenv("RUN_ADDRESS", tt.address)
			got, err := config.Load(nil)
			if (err == nil) != tt.valid {
				t.Fatalf("Load() error = %v, want valid = %v", err, tt.valid)
			}
			if tt.valid && got.RunAddress != tt.address {
				t.Errorf("RunAddress = %q, want %q", got.RunAddress, tt.address)
			}
		})
	}
}

func prepareConfig(t *testing.T, files map[string]string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, key := range []string{"RUN_ADDRESS", "DATABASE_URI", "ACCRUAL_SYSTEM_ADDRESS"} {
		t.Setenv(key, "")
	}
	for name, contents := range files {
		if err := os.WriteFile(name, []byte(contents), 0600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilterHostsRegex(t *testing.T) {
	hosts := []sshHost{
		{Alias: "prod", Hostname: "prod.example.com", User: "ubuntu", Notes: []string{"primary"}},
		{Alias: "stage", Hostname: "staging.example.com", User: "ec2-user"},
		{Alias: "db", Hostname: "10.0.0.5", User: "postgres", LocalForwards: []string{"5432"}},
	}

	t.Run("empty returns all", func(t *testing.T) {
		out, err := filterHostsRegex(hosts, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != len(hosts) {
			t.Fatalf("expected %d hosts, got %d", len(hosts), len(out))
		}
	})

	t.Run("matches alias", func(t *testing.T) {
		out, err := filterHostsRegex(hosts, "^prod$")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != 1 || out[0].Alias != "prod" {
			t.Fatalf("expected [prod], got %#v", out)
		}
	})

	t.Run("matches hostname", func(t *testing.T) {
		out, err := filterHostsRegex(hosts, "staging")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != 1 || out[0].Alias != "stage" {
			t.Fatalf("expected [stage], got %#v", out)
		}
	})

	t.Run("matches notes and forwards", func(t *testing.T) {
		out, err := filterHostsRegex(hosts, "primary|5432")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != 2 {
			t.Fatalf("expected 2 hosts, got %d", len(out))
		}
	})

	t.Run("invalid regex returns error", func(t *testing.T) {
		_, err := filterHostsRegex(hosts, "(")
		if err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestParseSSHConfig_SourceLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	content := `# top note

Host prod
  Hostname 127.0.0.1

Host stage other
  Hostname 127.0.0.1
`
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	hosts, err := parseSSHConfig(cfg)
	if err != nil {
		t.Fatalf("parseSSHConfig: %v", err)
	}

	byAlias := map[string]sshHost{}
	for _, h := range hosts {
		byAlias[h.Alias] = h
	}

	if got := byAlias["prod"].SourceLine; got != 3 {
		t.Fatalf("prod SourceLine: expected 3, got %d", got)
	}
	if got := byAlias["stage"].SourceLine; got != 6 {
		t.Fatalf("stage SourceLine: expected 6, got %d", got)
	}
	if got := byAlias["other"].SourceLine; got != 6 {
		t.Fatalf("other SourceLine: expected 6, got %d", got)
	}
}


package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, src string) *Config {
	t.Helper()
	cfg, err := Parse(strings.NewReader(src))
	require.NoError(t, err)
	return cfg
}

func parseErr(t *testing.T, src string) error {
	t.Helper()
	_, err := Parse(strings.NewReader(src))
	return err
}

// ── empty / defaults ──────────────────────────────────────────────────────────

func TestParse_EmptyFile_ReturnsDefaults(t *testing.T) {
	cfg := parse(t, "")
	d := Defaults()
	assert.Equal(t, d.Listen, cfg.Listen)
	assert.Equal(t, d.Workers, cfg.Workers)
	assert.Equal(t, d.TCP, cfg.TCP)
}

func TestParse_MinimalBlock(t *testing.T) {
	cfg := parse(t, "dns-edge {\n}\n")
	assert.Equal(t, Defaults().Listen, cfg.Listen)
}

// ── top-level keys ────────────────────────────────────────────────────────────

func TestParse_Listen(t *testing.T) {
	cfg := parse(t, "dns-edge {\n  listen :5300\n}\n")
	assert.Equal(t, ":5300", cfg.Listen)
}

func TestParse_Workers(t *testing.T) {
	cfg := parse(t, "dns-edge {\n  workers 4\n}\n")
	assert.Equal(t, 4, cfg.Workers)
}

func TestParse_TCP_True(t *testing.T) {
	for _, v := range []string{"true", "1", "yes"} {
		cfg := parse(t, "dns-edge {\n  tcp "+v+"\n}\n")
		assert.True(t, cfg.TCP, "value %q should be truthy", v)
	}
}

func TestParse_TCP_False(t *testing.T) {
	cfg := parse(t, "dns-edge {\n  tcp false\n}\n")
	assert.False(t, cfg.TCP)
}

func TestParse_EDNS0(t *testing.T) {
	cfg := parse(t, "dns-edge {\n  edns0 true\n}\n")
	assert.True(t, cfg.EDNS0)
}

// ── api block ─────────────────────────────────────────────────────────────────

func TestParse_APIBlock(t *testing.T) {
	src := `dns-edge {
  api {
    listen :8080
  }
}`
	cfg := parse(t, src)
	assert.Equal(t, ":8080", cfg.API.Listen)
}

// ── postgres block ────────────────────────────────────────────────────────────

func TestParse_PostgresBlock(t *testing.T) {
	src := `dns-edge {
  postgres {
    dsn "postgres://user:pass@localhost/dns"
  }
}`
	cfg := parse(t, src)
	assert.Equal(t, "postgres://user:pass@localhost/dns", cfg.PG.DSN)
}

// ── nacos block ───────────────────────────────────────────────────────────────

func TestParse_NacosBlock(t *testing.T) {
	src := `dns-edge {
  nacos {
    addr 127.0.0.1:8848
    namespace public
    group DEFAULT_GROUP
    data_id_prefix dns_weights:
    username admin
    password s3cr3t
  }
}`
	cfg := parse(t, src)
	assert.Equal(t, "127.0.0.1:8848", cfg.Nacos.Addr)
	assert.Equal(t, "public", cfg.Nacos.Namespace)
	assert.Equal(t, "DEFAULT_GROUP", cfg.Nacos.Group)
	assert.Equal(t, "dns_weights:", cfg.Nacos.DataIDPrefix)
	assert.Equal(t, "admin", cfg.Nacos.Username)
	assert.Equal(t, "s3cr3t", cfg.Nacos.Password)
}

// ── sync block ────────────────────────────────────────────────────────────────

func TestParse_SyncBlock(t *testing.T) {
	src := `dns-edge {
  sync {
    interval 10s
    prob 0.05
    ratelimit 50
  }
}`
	cfg := parse(t, src)
	assert.Equal(t, 10*time.Second, cfg.Sync.Interval)
	assert.InDelta(t, 0.05, cfg.Sync.Prob, 1e-9)
	assert.Equal(t, 50, cfg.Sync.RateLimit)
}

// ── inline comments ───────────────────────────────────────────────────────────

func TestParse_InlineComment(t *testing.T) {
	src := `dns-edge { # top comment
  listen :5353 # override
}`
	cfg := parse(t, src)
	assert.Equal(t, ":5353", cfg.Listen)
}

// ── error cases ───────────────────────────────────────────────────────────────

func TestParse_UnknownTopLevelKey_Error(t *testing.T) {
	err := parseErr(t, "dns-edge {\n  unknown_key value\n}\n")
	assert.Error(t, err)
}

func TestParse_WrongBlockName_Error(t *testing.T) {
	err := parseErr(t, "wrong-name {\n}\n")
	assert.Error(t, err)
}

func TestParse_WorkersNotInt_Error(t *testing.T) {
	err := parseErr(t, "dns-edge {\n  workers abc\n}\n")
	assert.Error(t, err)
}

func TestParse_SyncInvalidDuration_Error(t *testing.T) {
	err := parseErr(t, "dns-edge {\n  sync {\n    interval nope\n  }\n}\n")
	assert.Error(t, err)
}

func TestParse_UnterminatedString_Error(t *testing.T) {
	err := parseErr(t, "dns-edge {\n  listen \"unclosed\n}\n")
	assert.Error(t, err)
}

func TestParse_MissingOpenBrace_Error(t *testing.T) {
	err := parseErr(t, "dns-edge\n  listen :53\n}\n")
	assert.Error(t, err)
}

// ── quoted strings ────────────────────────────────────────────────────────────

func TestParse_QuotedDSN(t *testing.T) {
	src := "dns-edge {\n  postgres {\n    dsn \"host=localhost port=5432\"\n  }\n}\n"
	cfg := parse(t, src)
	assert.Equal(t, "host=localhost port=5432", cfg.PG.DSN)
}

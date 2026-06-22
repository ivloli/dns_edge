package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ParseFile reads a Corefile-style config from path.
func ParseFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Parse reads a Corefile-style config from r.
func Parse(r io.Reader) (*Config, error) {
	tokens, err := tokenize(r)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: tokens}
	return p.parse()
}

// ─── lexer ────────────────────────────────────────────────────────────────────

type tok struct {
	text string
	line int
}

func tokenize(r io.Reader) ([]tok, error) {
	var tokens []tok
	sc := bufio.NewScanner(r)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := sc.Text()

		// strip inline comment
		if ci := strings.IndexByte(line, '#'); ci >= 0 {
			line = line[:ci]
		}

		toks, err := lexLine(line, lineNum)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		tokens = append(tokens, toks...)
	}
	return tokens, sc.Err()
}

// lexLine splits one source line into tokens, handling:
//   - { and } as single-character tokens
//   - "…" quoted strings (content between quotes, backslash not supported)
//   - bare words (any non-whitespace sequence that isn't a brace)
func lexLine(line string, lineNum int) ([]tok, error) {
	var out []tok
	i := 0
	for i < len(line) {
		// skip whitespace
		r, sz := utf8.DecodeRuneInString(line[i:])
		if r == ' ' || r == '\t' || r == '\r' {
			i += sz
			continue
		}

		switch line[i] {
		case '{', '}':
			out = append(out, tok{text: string(line[i]), line: lineNum})
			i++

		case '"':
			// scan to closing quote
			j := i + 1
			for j < len(line) && line[j] != '"' {
				j++
			}
			if j >= len(line) {
				return nil, fmt.Errorf("unterminated quoted string")
			}
			out = append(out, tok{text: line[i+1 : j], line: lineNum})
			i = j + 1

		default:
			// bare word: everything up to whitespace or brace
			j := i
			for j < len(line) && line[j] != ' ' && line[j] != '\t' &&
				line[j] != '\r' && line[j] != '{' && line[j] != '}' && line[j] != '"' {
				j++
			}
			out = append(out, tok{text: line[i:j], line: lineNum})
			i = j
		}
	}
	return out, nil
}

// ─── parser ───────────────────────────────────────────────────────────────────

type parser struct {
	tokens []tok
	pos    int
}

func (p *parser) peek() (tok, bool) {
	if p.pos >= len(p.tokens) {
		return tok{}, false
	}
	return p.tokens[p.pos], true
}

func (p *parser) next() (tok, bool) {
	t, ok := p.peek()
	if ok {
		p.pos++
	}
	return t, ok
}

func (p *parser) expect(s string) error {
	t, ok := p.next()
	if !ok {
		return fmt.Errorf("unexpected EOF, expected %q", s)
	}
	if t.text != s {
		return fmt.Errorf("line %d: expected %q, got %q", t.line, s, t.text)
	}
	return nil
}

func (p *parser) nextVal(key tok) (string, error) {
	t, ok := p.next()
	if !ok || t.text == "}" {
		return "", fmt.Errorf("line %d: %q requires a value", key.line, key.text)
	}
	return t.text, nil
}

func (p *parser) parse() (*Config, error) {
	cfg := Defaults()

	name, ok := p.next()
	if !ok {
		return cfg, nil // empty file → all defaults
	}
	if name.text != "dns-edge" {
		return nil, fmt.Errorf("line %d: expected block name \"dns-edge\", got %q", name.line, name.text)
	}
	if err := p.expect("{"); err != nil {
		return nil, err
	}
	if err := p.parseMain(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (p *parser) parseMain(cfg *Config) error {
	for {
		t, ok := p.peek()
		if !ok {
			return fmt.Errorf("unexpected EOF inside dns-edge block")
		}
		if t.text == "}" {
			p.next()
			return nil
		}

		key, _ := p.next()
		switch key.text {
		case "listen":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.Listen = v

		case "workers":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("line %d: workers must be an integer: %v", key.line, err)
			}
			cfg.Workers = n

		case "tcp":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.TCP = isTruthy(v)

		case "edns0":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.EDNS0 = isTruthy(v)

		case "api":
			if err := p.expect("{"); err != nil {
				return err
			}
			if err := p.parseAPI(&cfg.API); err != nil {
				return err
			}

		case "postgres":
			if err := p.expect("{"); err != nil {
				return err
			}
			if err := p.parsePG(&cfg.PG); err != nil {
				return err
			}

		case "nacos":
			if err := p.expect("{"); err != nil {
				return err
			}
			if err := p.parseNacos(&cfg.Nacos); err != nil {
				return err
			}

		case "sync":
			if err := p.expect("{"); err != nil {
				return err
			}
			if err := p.parseSync(&cfg.Sync); err != nil {
				return err
			}

		default:
			return fmt.Errorf("line %d: unknown key %q in dns-edge block", key.line, key.text)
		}
	}
}

func (p *parser) parseAPI(cfg *APIConfig) error {
	for {
		t, ok := p.peek()
		if !ok {
			return fmt.Errorf("unexpected EOF inside api block")
		}
		if t.text == "}" {
			p.next()
			return nil
		}
		key, _ := p.next()
		switch key.text {
		case "listen":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.Listen = v
		default:
			return fmt.Errorf("line %d: unknown key %q in api block", key.line, key.text)
		}
	}
}

func (p *parser) parsePG(cfg *PGConfig) error {
	for {
		t, ok := p.peek()
		if !ok {
			return fmt.Errorf("unexpected EOF inside postgres block")
		}
		if t.text == "}" {
			p.next()
			return nil
		}
		key, _ := p.next()
		switch key.text {
		case "dsn":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.DSN = v
		default:
			return fmt.Errorf("line %d: unknown key %q in postgres block", key.line, key.text)
		}
	}
}

func (p *parser) parseNacos(cfg *NacosConfig) error {
	for {
		t, ok := p.peek()
		if !ok {
			return fmt.Errorf("unexpected EOF inside nacos block")
		}
		if t.text == "}" {
			p.next()
			return nil
		}
		key, _ := p.next()
		switch key.text {
		case "addr":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.Addr = v
		case "namespace":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.Namespace = v
		case "group":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.Group = v
		case "data_id_prefix":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.DataIDPrefix = v
		case "username":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.Username = v
		case "password":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			cfg.Password = v
		default:
			return fmt.Errorf("line %d: unknown key %q in nacos block", key.line, key.text)
		}
	}
}

func (p *parser) parseSync(cfg *SyncConfig) error {
	for {
		t, ok := p.peek()
		if !ok {
			return fmt.Errorf("unexpected EOF inside sync block")
		}
		if t.text == "}" {
			p.next()
			return nil
		}
		key, _ := p.next()
		switch key.text {
		case "interval":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("line %d: invalid duration %q: %v", key.line, v, err)
			}
			cfg.Interval = d
		case "prob":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("line %d: invalid float %q: %v", key.line, v, err)
			}
			cfg.Prob = f
		case "ratelimit":
			v, err := p.nextVal(key)
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("line %d: ratelimit must be an integer: %v", key.line, err)
			}
			cfg.RateLimit = n
		default:
			return fmt.Errorf("line %d: unknown key %q in sync block", key.line, key.text)
		}
	}
}

func isTruthy(s string) bool {
	return s == "true" || s == "1" || s == "yes"
}

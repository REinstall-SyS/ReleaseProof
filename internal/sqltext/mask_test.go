package sqltext

import (
	"strings"
	"testing"
)

func TestMaskHidesQuotedAndCommentedBackslashes(t *testing.T) {
	for _, input := range []string{
		`SELECT '\path', '\\literal', 'it''s fine';`,
		`SELECT E'escaped\' quote \\ still literal';`,
		`SELECT "identifier\path";`,
		`DO $body$ BEGIN RAISE NOTICE '\! harmless'; END $body$;`,
		"-- \\! harmless\nSELECT 1; /* nested /* \\gexec */ comment */",
		"SELECT E'first'\n'escaped\\' quote';",
	} {
		masked, complete := Mask(input)
		if !complete || strings.ContainsRune(masked, '\\') {
			t.Fatalf("literal/comment was not masked: %q -> %q (complete=%v)", input, masked, complete)
		}
		if len(masked) != len(input) {
			t.Fatal("mask changed byte positions")
		}
	}
}

func TestMaskLeavesInlineCommandsVisible(t *testing.T) {
	for _, input := range []string{
		`SELECT 1; \! command`,
		`SELECT 'ordinary\'; \gexec`,
		`SELECT E'escaped\' quote'; \! command`,
		`SELECT $$\! literal$$; \gexec`,
		"-- comment\r\\! command",
		`SELECT some$tag$identifier; \gexec`,
	} {
		masked, complete := Mask(input)
		if !complete || !strings.ContainsRune(masked, '\\') {
			t.Fatalf("command was hidden: %q -> %q (complete=%v)", input, masked, complete)
		}
	}
}

func TestMaskRejectsUnterminatedConstructs(t *testing.T) {
	for _, input := range []string{`SELECT 'unfinished`, `SELECT E'escaped\'`, `SELECT "unfinished`, `DO $body$ unfinished`, `/* unfinished /* nested */`} {
		if _, complete := Mask(input); complete {
			t.Fatalf("unterminated construct accepted: %q", input)
		}
	}
}

func TestMaskUsesStandardStringRules(t *testing.T) {
	// Under standard_conforming_strings=on, backslash does not escape the
	// closing quote; the command after it must remain visible.
	masked, complete := Mask(`SELECT '\'; \! command`)
	if !complete || !strings.Contains(masked, `\! command`) {
		t.Fatalf("ordinary string used E-string escaping: %q (%v)", masked, complete)
	}
	// An E prefix embedded in an identifier does not enable backslash escapes.
	masked, complete = Mask(`SELECT nameE'\'; \! command`)
	if !complete || !strings.Contains(masked, `\! command`) {
		t.Fatalf("identifier suffix became an E prefix: %q (%v)", masked, complete)
	}
}

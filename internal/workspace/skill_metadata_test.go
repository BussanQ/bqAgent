package workspace

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseSkillMetadataMultilineDescriptions(t *testing.T) {
	for _, indicator := range []string{"|", ">", "|-", ">-", "|+", ">+", "|2", ">2-", "|-2", "| # summary"} {
		t.Run(indicator, func(t *testing.T) {
			content := "---\nname: demo\ndescription: " + indicator + "\n  第一行：解释主题。\n\n  第二行带图片。\n  alias: 这仍是描述\nalias: explain\naliases:\n  - simple\n---\nPrivate workflow body."
			description, aliases := parseSkillMetadata(content)
			if description != "第一行：解释主题。 第二行带图片。 alias: 这仍是描述" {
				t.Fatalf("description = %q", description)
			}
			if !slices.Equal(aliases, []string{"explain", "simple"}) {
				t.Fatalf("aliases = %v", aliases)
			}
		})
	}
}

func TestParseSkillMetadataEscapedDelimitersAndFallbacks(t *testing.T) {
	for _, test := range []struct {
		name, content, want string
	}{
		{"escaped", "\\---  \ndescription: Explain a topic simply.  \n\\---\nPrivate body", "Explain a topic simply."},
		{"mixed CRLF", "\\---\r\ndescription: >-\r\n  First line.\r\n  Second line.\r\n---\r\nPrivate body", "First line. Second line."},
		{"single line unchanged", "---\ndescription: 'Simple explanations.'\n---", "Simple explanations."},
		{"quoted block symbol is text", "---\ndescription: '| symbol'\n---", "| symbol"},
		{"no frontmatter", "# Skill\ndescription: Private body", defaultSkillDescription},
		{"unclosed", "---\ndescription: |\n  Never expose partial metadata.\nalias: explain", defaultSkillDescription},
		{"empty block", "---\ndescription: |\n\nname: demo\n---\nPrivate body", defaultSkillDescription},
		{"empty block at delimiter", "---\ndescription: >\n\\---\nPrivate body", defaultSkillDescription},
	} {
		t.Run(test.name, func(t *testing.T) {
			description, aliases := parseSkillMetadata(test.content)
			if description != test.want || len(aliases) != 0 {
				t.Fatalf("description = %q, aliases = %v; want %q", description, aliases, test.want)
			}
		})
	}
}

func TestParseSkillMetadataMultilineDescriptionLimit(t *testing.T) {
	content := "---\ndescription: |\n  " + strings.Repeat("中文", maxSkillDescriptionBytes) + "\n  More text.\nalias: explain\n---\nPrivate body"
	description, aliases := parseSkillMetadata(content)
	if len(description) > maxSkillDescriptionBytes || len(description) < maxSkillDescriptionBytes-3 || !utf8.ValidString(description) {
		t.Fatalf("invalid description truncation: bytes=%d valid=%t", len(description), utf8.ValidString(description))
	}
	if !slices.Equal(aliases, []string{"explain"}) {
		t.Fatalf("aliases = %v", aliases)
	}
}

package server

import (
	"strings"
	"testing"
)

func TestIsMarkdown(t *testing.T) {
	yes := []string{"report.md", "REPORT.MD", "notes.markdown", "a/b/c.md"}
	no := []string{"chart.png", "page.html", "data.json", "noext"}

	for _, n := range yes {
		if !isMarkdown(n) {
			t.Errorf("isMarkdown(%q) = false, want true", n)
		}
	}

	for _, n := range no {
		if isMarkdown(n) {
			t.Errorf("isMarkdown(%q) = true, want false", n)
		}
	}
}

func TestRenderMarkdownProducesSanitizedHTML(t *testing.T) {
	name, html, err := renderMarkdown("finality.md", []byte("# Finality delay\n\n| slot | status |\n|---|---|\n| 42 | late |\n"))
	if err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}

	if name != "finality.html" {
		t.Errorf("name = %q, want finality.html", name)
	}

	doc := string(html)
	for _, want := range []string{"<!doctype html>", "<h1", "Finality delay", "<table>", "<td>late</td>"} {
		if !strings.Contains(doc, want) {
			t.Errorf("rendered doc missing %q\n%s", want, doc)
		}
	}
}

func TestRenderMarkdownStripsActiveContent(t *testing.T) {
	src := []byte("# Hi\n\n<script>alert(1)</script>\n\n[click](javascript:alert(2))\n\n<img src=x onerror=alert(3)>\n")

	_, html, err := renderMarkdown("evil.md", src)
	if err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}

	doc := strings.ToLower(string(html))
	for _, forbidden := range []string{"<script", "javascript:", "onerror"} {
		if strings.Contains(doc, forbidden) {
			t.Errorf("rendered doc contains %q — sanitization failed\n%s", forbidden, doc)
		}
	}
}

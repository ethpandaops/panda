package server

import (
	"bytes"
	"fmt"
	"html/template"
	"path"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// markdownConverter renders GitHub-flavored markdown. Raw HTML in the source is
// escaped (WithUnsafe is deliberately not set), so the only markup that reaches
// the output is what goldmark itself emits.
var markdownConverter = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
)

// markdownSanitizer strips anything unsafe (scripts, event handlers,
// javascript:/data: URLs) from the rendered HTML — defense in depth on top of
// goldmark's escaping, so a hostile document can't smuggle active content.
var markdownSanitizer = bluemonday.UGCPolicy()

func isMarkdown(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return true
	default:
		return false
	}
}

// renderMarkdown converts a markdown source into a self-contained, sanitized
// HTML document and returns it under an .html name so downstream content-type
// derivation resolves to text/html.
func renderMarkdown(name string, src []byte) (string, []byte, error) {
	var body bytes.Buffer
	if err := markdownConverter.Convert(src, &body); err != nil {
		return "", nil, fmt.Errorf("converting markdown: %w", err)
	}

	safe := markdownSanitizer.SanitizeBytes(body.Bytes())

	var doc bytes.Buffer
	if err := markdownDocTmpl.Execute(&doc, markdownDocData{
		Title: strings.TrimSuffix(name, path.Ext(name)),
		Body:  template.HTML(safe), //nolint:gosec // sanitized by bluemonday above
	}); err != nil {
		return "", nil, fmt.Errorf("wrapping markdown: %w", err)
	}

	htmlName := strings.TrimSuffix(name, path.Ext(name)) + ".html"

	return htmlName, doc.Bytes(), nil
}

type markdownDocData struct {
	Title string
	Body  template.HTML
}

var markdownDocTmpl = template.Must(template.New("markdown").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
 body{font-family:system-ui,-apple-system,sans-serif;line-height:1.6;max-width:820px;margin:2.5rem auto;padding:0 1.25rem;color:#1a1a1a}
 h1,h2,h3{line-height:1.25;margin-top:2rem}
 h1{border-bottom:1px solid #eaecef;padding-bottom:.3rem}
 h2{border-bottom:1px solid #eaecef;padding-bottom:.3rem}
 code{background:#f5f5f5;padding:.15em .4em;border-radius:4px;font-size:.9em}
 pre{background:#f6f8fa;padding:1rem;border-radius:8px;overflow-x:auto}
 pre code{background:none;padding:0}
 table{border-collapse:collapse;width:100%;margin:1rem 0}
 th,td{border:1px solid #d0d7de;padding:.4rem .75rem;text-align:left}
 th{background:#f6f8fa}
 blockquote{margin:1rem 0;padding:0 1rem;color:#57606a;border-left:.25rem solid #d0d7de}
 img{max-width:100%}
 a{color:#0969da}
 @media(prefers-color-scheme:dark){
  body{background:#0d1117;color:#e6edf3}
  h1,h2{border-color:#21262d}
  code{background:#161b22}
  pre{background:#161b22}
  th,td{border-color:#30363d}
  th{background:#161b22}
  blockquote{color:#8b949e;border-color:#30363d}
  a{color:#4493f8}
 }
</style>
</head>
<body>
{{.Body}}
</body>
</html>`))

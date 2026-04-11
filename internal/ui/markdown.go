package ui

import (
	"bytes"
	"io"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	g "maragu.dev/gomponents"
)

var codexMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
)

func codexMarkdownNode(source string) g.Node {
	return g.NodeFunc(func(w io.Writer) error {
		var rendered bytes.Buffer
		if err := codexMarkdown.Convert([]byte(source), &rendered); err != nil {
			return err
		}
		_, err := io.WriteString(w, rendered.String())
		return err
	})
}

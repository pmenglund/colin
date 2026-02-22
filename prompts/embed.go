package prompts

import _ "embed"

// WorkMarkdown contains the embedded default work prompt template.
//
//go:embed work.md
var WorkMarkdown string

// MergeMarkdown contains the embedded default merge prompt template.
//
//go:embed merge.md
var MergeMarkdown string

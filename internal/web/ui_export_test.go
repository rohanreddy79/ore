// SPDX-License-Identifier: AGPL-3.0-only

package web

import "html/template"

// Exported for the UI tests in package web_test.

// RenderDiff exposes the diff renderer.
func RenderDiff(text string) (template.HTML, int, int) {
	h, st := renderDiff(text)
	return h, st.Files, st.Collapsed
}

// TruncateLine exposes the log line limit.
func TruncateLine(l string) string { return truncateLine(l) }

// MaxLogLine is the line limit.
const MaxLogLine = maxLogLine

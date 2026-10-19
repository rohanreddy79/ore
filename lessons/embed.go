// SPDX-License-Identifier: AGPL-3.0-only

// Package lessons embeds the built-in curriculum (CC BY-SA 4.0, see
// lessons/README.md) into the binary so `ore learn` works offline and in
// air-gapped installations. The engine in internal/learn loads it through
// FS and can add lessons from ORE_LESSONS_DIR at run time.
package lessons

import "embed"

// FS holds lessons/tracks/<track>/<lesson>/{lesson.yaml,steps/<lang>.md}.
//
//go:embed tracks
var FS embed.FS

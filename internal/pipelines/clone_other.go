// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin

package pipelines

import "errors"

// cloneDir has no single-call directory clone outside APFS; cloneTree falls
// back to a per-file copy (reflinked where the filesystem supports it).
func cloneDir(src, dst string) error { return errors.ErrUnsupported }

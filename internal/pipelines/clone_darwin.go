// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package pipelines

import "golang.org/x/sys/unix"

// cloneDir clones a whole directory tree in one clonefile(2) call on APFS:
// tens of milliseconds for 10,000 files where a per-file copy takes seconds.
// dst must not exist.
func cloneDir(src, dst string) error {
	return unix.Clonefile(src, dst, 0)
}

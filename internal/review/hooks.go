// SPDX-License-Identifier: AGPL-3.0-only

package review

// beforeCAS, when set (tests only), runs right before a server-side merge's
// compare-and-swap ref update so a test can move the target underneath and
// exercise the retry path.
var beforeCAS func(ref string)

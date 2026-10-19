// SPDX-License-Identifier: AGPL-3.0-only

package github

import "github.com/orevcs/ore/internal/registry"

func init() { registry.Register("fpi.github", Install) }

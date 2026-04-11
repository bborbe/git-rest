// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import "errors"

func errWithMessage(msg string) error {
	return errors.New(msg)
}

/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package externaldns

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestExternalDNS(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ExternalDNS Webhook Suite")
}

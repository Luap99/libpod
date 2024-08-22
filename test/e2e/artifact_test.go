//go:build linux || freebsd

package integration

import (
	"encoding/json"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/oci/layout"
	"github.com/containers/podman/v5/pkg/domain/entities"
	. "github.com/containers/podman/v5/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	imageSingle   = "quay.io/baude/artifact:single"
	imageMultiple = "quay.io/baude/artifact:multiple"
)

type tmpArtifact struct {
	List      layout.ListResult
	Manifests []manifest.OCI1
	/*
		List      layout.ListResult
		Manifests []manifest.OCI1
	*/
}

var _ = Describe("Podman artifact", func() {
	It("podman artifact basic tests", func() {
		// Pull
		sessionSingle := podmanTest.Podman([]string{"artifact", "pull", imageSingle})
		sessionSingle.WaitWithDefaultTimeout()
		Expect(sessionSingle).Should(ExitCleanly())
		Expect(sessionSingle.OutputToStringArray()).ToNot(BeEmpty())
		sessionMultiple := podmanTest.Podman([]string{"artifact", "pull", imageMultiple})
		sessionMultiple.WaitWithDefaultTimeout()
		Expect(sessionMultiple).Should(ExitCleanly())

		// List
		listSession := podmanTest.Podman([]string{"artifact", "ls"})
		listSession.WaitWithDefaultTimeout()
		Expect(listSession).Should(ExitCleanly())
		Expect(len(listSession.OutputToStringArray())).To(Equal(3))

		// Inspect
		inspectSingleSession := podmanTest.Podman([]string{"artifact", "inspect", imageSingle})
		inspectSingleSession.WaitWithDefaultTimeout()
		Expect(inspectSingleSession).Should(ExitCleanly())
		ta := entities.ArtifactInspectReport{}
		inspectOut := inspectSingleSession.OutputToString()
		err := json.Unmarshal([]byte(inspectOut), &ta)
		Expect(err).To(BeNil())

	})

})

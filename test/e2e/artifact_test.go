//go:build linux || freebsd

package integration

import (
	"encoding/json"
	"fmt"

	"github.com/containers/podman/v5/pkg/libartifact"
	. "github.com/containers/podman/v5/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gexec"
)

var (
	// TODO These images should be replaced by images already in the e2e registry
	imageSingle   = "quay.io/baude/artifact:single"
	imageMultiple = "quay.io/baude/artifact:multiple"
)

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
		Expect(listSession.OutputToStringArray()).To(HaveLen(3))

		// Remove
		removeSession := podmanTest.Podman([]string{"artifact", "rm", imageMultiple})
		removeSession.WaitWithDefaultTimeout()
		Expect(removeSession).Should(ExitCleanly())

		removeListSession := podmanTest.Podman([]string{"artifact", "ls"})
		removeListSession.WaitWithDefaultTimeout()
		Expect(removeListSession).Should(ExitCleanly())
		Expect(removeListSession.OutputToStringArray()).To(HaveLen(2))

		// Inspect
		inspectSingleSession := podmanTest.Podman([]string{"artifact", "inspect", imageSingle})
		inspectSingleSession.WaitWithDefaultTimeout()
		Expect(inspectSingleSession).Should(ExitCleanly())

		a := libartifact.Artifact{}
		inspectOut := inspectSingleSession.OutputToString()
		err = json.Unmarshal([]byte(inspectOut), &a)
		Expect(err).ToNot(HaveOccurred())
		Expect(a.Name).To(Equal(imageSingle))

	})

	It("podman artifact ls", func() {
		artifact1File, err := createArtifactFile(4192)
		Expect(err).ToNot(HaveOccurred())
		artifact1Name := "localhost/test/artifact1"
		addArtifact1 := podmanTest.Podman([]string{"artifact", "add", artifact1Name, artifact1File})
		addArtifact1.WaitWithDefaultTimeout()
		Expect(addArtifact1).To(ExitCleanly())

		artifact2File, err := createArtifactFile(10240)
		Expect(err).ToNot(HaveOccurred())
		artifact2Name := "localhost/test/artifact2"
		addArtifact2 := podmanTest.Podman([]string{"artifact", "add", artifact2Name, artifact2File})
		addArtifact2.WaitWithDefaultTimeout()
		Expect(addArtifact1).To(ExitCleanly())

		// Should be three items in the list
		listSession := podmanTest.Podman([]string{"artifact", "ls"})
		listSession.WaitWithDefaultTimeout()
		Expect(listSession).Should(ExitCleanly())
		Expect(listSession.OutputToStringArray()).To(HaveLen(2))

		// --format should work
		listFormatSession := podmanTest.Podman([]string{"artifact", "ls", "--format", "{{.Name}}"})
		listFormatSession.WaitWithDefaultTimeout()
		Expect(listFormatSession).Should(ExitCleanly())
		output := listFormatSession.OutputToStringArray()

		// There should be only 2 "lines" because the header should not be output
		Expect(output).To(HaveLen(2))

		// Make sure the names are what we expect
		Expect(output).To(ContainElement(artifact1Name))
		Expect(output).To(ContainElement(artifact2Name))
	})

	It("podman artifact simple add", func() {
		artifact1File, err := createArtifactFile(1024)
		Expect(err).ToNot(HaveOccurred())

		artifact1Name := "localhost/test/artifact1"
		addArtifact1 := podmanTest.Podman([]string{"artifact", "add", artifact1Name, artifact1File})
		addArtifact1.WaitWithDefaultTimeout()
		Expect(addArtifact1).To(ExitCleanly())

		inspectSingleSession := podmanTest.Podman([]string{"artifact", "inspect", artifact1Name})
		inspectSingleSession.WaitWithDefaultTimeout()
		Expect(inspectSingleSession).Should(ExitCleanly())

		a := libartifact.Artifact{}
		inspectOut := inspectSingleSession.OutputToString()
		err = json.Unmarshal([]byte(inspectOut), &a)
		Expect(err).ToNot(HaveOccurred())
		Expect(a.Name).To(Equal(artifact1Name))

		// Adding an artifact with an existing name should fail
		addAgain := podmanTest.Podman([]string{"artifact", "add", artifact1Name, artifact1File})
		addAgain.WaitWithDefaultTimeout()
		Expect(addAgain).ShouldNot(ExitCleanly())
	})

	It("podman artifact add multiple", func() {
		artifact1File1, err := createArtifactFile(1024)
		Expect(err).ToNot(HaveOccurred())
		artifact1File2, err := createArtifactFile(8192)
		Expect(err).ToNot(HaveOccurred())

		artifact1Name := "localhost/test/artifact1"

		addArtifact1 := podmanTest.Podman([]string{"artifact", "add", artifact1Name, artifact1File1, artifact1File2})
		addArtifact1.WaitWithDefaultTimeout()
		Expect(addArtifact1).To(ExitCleanly())

		inspectSingleSession := podmanTest.Podman([]string{"artifact", "inspect", artifact1Name})
		inspectSingleSession.WaitWithDefaultTimeout()
		Expect(inspectSingleSession).Should(ExitCleanly())

		a := libartifact.Artifact{}
		inspectOut := inspectSingleSession.OutputToString()
		err = json.Unmarshal([]byte(inspectOut), &a)
		Expect(err).ToNot(HaveOccurred())
		Expect(a.Name).To(Equal(artifact1Name))

		var layerCount int
		for _, layer := range a.Manifests {
			layerCount += len(layer.Layers)
		}
		Expect(layerCount).To(Equal(2))
	})

	It("podman artifact push and pull", func() {
		artifact1File, err := createArtifactFile(1024)
		Expect(err).ToNot(HaveOccurred())

		lock, port, err := setupRegistry(nil)
		if err == nil {
			defer lock.Unlock()
		}
		Expect(err).ToNot(HaveOccurred())

		artifact1Name := fmt.Sprintf("localhost:%s/test/artifact1", port)
		addArtifact1 := podmanTest.Podman([]string{"artifact", "add", artifact1Name, artifact1File})
		addArtifact1.WaitWithDefaultTimeout()
		Expect(addArtifact1).To(ExitCleanly())

		push := podmanTest.Podman([]string{"artifact", "push", "--tls-verify=false", artifact1Name})
		push.WaitWithDefaultTimeout()
		Expect(push).Should(ExitCleanly())

		rmArtifact := podmanTest.Podman([]string{"artifact", "rm", artifact1Name})
		rmArtifact.WaitWithDefaultTimeout()
		Expect(rmArtifact).Should(ExitCleanly())

		pullArtifact := podmanTest.Podman([]string{"artifact", "pull", "--tls-verify=false", artifact1Name})
		pullArtifact.WaitWithDefaultTimeout()
		Expect(pullArtifact).Should(ExitCleanly())

		inspectSingleSession := podmanTest.Podman([]string{"artifact", "inspect", artifact1Name})
		inspectSingleSession.WaitWithDefaultTimeout()
		Expect(inspectSingleSession).Should(ExitCleanly())

		a := libartifact.Artifact{}
		inspectOut := inspectSingleSession.OutputToString()
		err = json.Unmarshal([]byte(inspectOut), &a)
		Expect(err).ToNot(HaveOccurred())
		Expect(a.Name).To(Equal(artifact1Name))
	})

	It("podman artifact remove", func() {
		// Trying to remove an image that does not exist should fail
		rmFail := podmanTest.Podman([]string{"artifact", "rm", imageSingle})
		rmFail.WaitWithDefaultTimeout()
		Expect(rmFail).Should(Exit(125))

		// Add an artifact to remove later
		artifact1File, err := createArtifactFile(4192)
		Expect(err).ToNot(HaveOccurred())
		artifact1Name := "localhost/test/artifact1"
		addArtifact1 := podmanTest.Podman([]string{"artifact", "add", artifact1Name, artifact1File})
		addArtifact1.WaitWithDefaultTimeout()
		Expect(addArtifact1).To(ExitCleanly())

		// Removing that artifact should work
		rmWorks := podmanTest.Podman([]string{"artifact", "rm", artifact1Name})
		rmWorks.WaitWithDefaultTimeout()
		Expect(rmWorks).To(ExitCleanly())

		// Inspecting that the removed artifact should fail
		inspectArtifact := podmanTest.Podman([]string{"artifact", "inspect", artifact1Name})
		inspectArtifact.WaitWithDefaultTimeout()
		Expect(inspectArtifact).Should(Exit(125))
	})
})

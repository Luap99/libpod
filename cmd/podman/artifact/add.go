package artifact

import (
	"fmt"

	"github.com/containers/podman/v5/cmd/podman/common"
	"github.com/containers/podman/v5/cmd/podman/registry"
	"github.com/containers/podman/v5/pkg/domain/entities"
	"github.com/spf13/cobra"
)

var (
	addCmd = &cobra.Command{
		Use:               "add [options] ARTIFACT PATH [...PATH]",
		Short:             "Add an OCI artifact to the local store",
		Long:              "Add an OCI artifact to the local store from the local filesystem",
		RunE:              add,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: common.AutocompleteArtifactAdd,
		Example:           `podman artifact add quay.io/myimage/myartifact:latest /tmp/foobar.txt`,
	}
)

func init() {
	registry.Commands = append(registry.Commands, registry.CliCommand{
		Command: addCmd,
		Parent:  artifactCmd,
	})

	// TODO When the inspect structure has been defined, we need to uncommand and redirect this.  Reminder, this
	// will also need to be reflected in the podman-artifact-inspect man page
	// _ = inspectCmd.RegisterFlagCompletionFunc(formatFlagName, common.AutocompleteFormat(&machine.InspectInfo{}))
}

func add(cmd *cobra.Command, args []string) error {
	report, err := registry.ImageEngine().ArtifactAdd(registry.Context(), args[0], args[1:], entities.ArtifactAddoptions{})
	if err != nil {
		return err
	}
	fmt.Println(report.ArtifactDigest)
	return nil
}

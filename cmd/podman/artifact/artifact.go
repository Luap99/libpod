package artifact

import (
	"github.com/containers/podman/v5/cmd/podman/registry"
	"github.com/containers/podman/v5/cmd/podman/validate"
	"github.com/spf13/cobra"
)

var (
	json = registry.JSONLibrary()
	// Command: podman _artifact_
	artifactCmd = &cobra.Command{
		Use:   "artifact",
		Short: "Manage OCI artifacts",
		Long:  "Manage OCI artifacts",
		//PersistentPreRunE: validate.NoOp,
		RunE: validate.SubCommandExists,
	}
)

func init() {
	registry.Commands = append(registry.Commands, registry.CliCommand{
		Command: artifactCmd,
	})
}
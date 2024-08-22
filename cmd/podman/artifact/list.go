package artifact

import (
	"fmt"
	"os"

	"github.com/containers/common/pkg/completion"
	"github.com/containers/common/pkg/report"
	"github.com/containers/podman/v5/cmd/podman/registry"
	"github.com/containers/podman/v5/pkg/domain/entities"
	"github.com/spf13/cobra"
)

var (
	ListCmd = &cobra.Command{
		Use:               "list [options]",
		Aliases:           []string{"ls"},
		Short:             "List OCI artifacts",
		Long:              "List OCI artifacts in local store",
		RunE:              list,
		Args:              cobra.NoArgs,
		ValidArgsFunction: completion.AutocompleteNone,
		Example:           `podman artifact ls`,
	}
	listFlag = listFlagType{}
)

type listFlagType struct {
	format string
}

type artifactListOutput struct {
	Name string
	Size int64
}

var (
	defaultArtifactListOutputFormat = "{{range .}}{{.Name}}\t{{.Size}}\n{{end -}}"
)

func init() {
	registry.Commands = append(registry.Commands, registry.CliCommand{
		Command: ListCmd,
		Parent:  artifactCmd,
	})
	flags := ListCmd.Flags()
	formatFlagName := "format"
	flags.StringVar(&listFlag.format, formatFlagName, defaultArtifactListOutputFormat, "Format volume output using JSON or a Go template")

	// TODO When the inspect structure has been defined, we need to uncomment and redirect this.  Reminder, this
	// will also need to be reflected in the podman-artifact-inspect man page
	// _ = inspectCmd.RegisterFlagCompletionFunc(formatFlagName, common.AutocompleteFormat(&machine.InspectInfo{}))
}

func list(cmd *cobra.Command, _ []string) error {
	reports, err := registry.ImageEngine().ArtifactList(registry.GetContext(), entities.ArtifactListOptions{})
	if err != nil {
		return err
	}

	return outputTemplate(cmd, reports)
}

func outputTemplate(cmd *cobra.Command, lrs []*entities.ArtifactListReport) error {
	var err error
	artifacts := make([]artifactListOutput, 0)
	for _, lr := range lrs {
		artifactName, err := lr.Artifact.GetName()
		// if we want to implement the concept of None, the above func
		// call might be where to do it
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifactListOutput{
			Name: artifactName,
			Size: lr.Artifact.TotalSize(),
		})
	}

	headers := report.Headers(artifactListOutput{}, map[string]string{
		"Name": "NAME",
		"Size": "SIZE",
	})

	rpt := report.New(os.Stdout, cmd.Name())
	defer rpt.Flush()

	switch {
	case cmd.Flag("format").Changed:
		rpt, err = rpt.Parse(report.OriginUser, listFlag.format)
	default:
		rpt, err = rpt.Parse(report.OriginPodman, listFlag.format)
	}
	if err != nil {
		return err
	}

	if rpt.RenderHeaders {
		if err := rpt.Execute(headers); err != nil {
			return fmt.Errorf("failed to write report column headers: %w", err)
		}
	}
	return rpt.Execute(artifacts)
}

package libartifact

import (
	"fmt"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/oci/layout"
	"github.com/containers/podman/v5/pkg/libartifact/types"
)

type Artifact struct {
	List      layout.ListResult
	Manifests []manifest.OCI1
}

// TotalSize returns the total bytes of the all the artifact layers
func (a Artifact) TotalSize() int64 {
	var s int64
	for _, artifact := range a.Manifests {
		for _, layer := range artifact.Layers {
			s += layer.Size
		}
	}
	return s
}

type ArtifactList []*Artifact

// GetByName returns an artifact, if present, by a given name
func (al ArtifactList) GetByName(name string) (*Artifact, error) {
	for _, artifact := range al {
		if val, ok := artifact.List.ManifestDescriptor.Annotations[types.AnnotatedName]; ok && val == name {
			return artifact, nil
		}
	}
	return nil, fmt.Errorf("no artifact found with name %s", name)
}

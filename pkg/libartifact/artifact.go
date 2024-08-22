package libartifact

import (
	"errors"
	"fmt"

	"github.com/containers/image/v5/manifest"
)

type Artifact struct {
	Manifests []manifest.OCI1
	Name      string
}

// TotalSize returns the total bytes of the all the artifact layers
func (a *Artifact) TotalSize() int64 {
	var s int64
	for _, artifact := range a.Manifests {
		for _, layer := range artifact.Layers {
			s += layer.Size
		}
	}
	return s
}

// GetName returns the "name" or "image reference" of the artifact
func (a *Artifact) GetName() (string, error) {
	if a.Name != "" {
		return a.Name, nil
	}
	// We don't have a concept of None for artifacts yet, but if we do,
	// then we should probably not error but return `None`
	return "", errors.New("artifact is unnamed")
}

// SetName is a accessor for setting the artifact name
// Note: long term this may not be needed, and we would
// be comfortable with simply using the exported field
// called Name
func (a *Artifact) SetName(name string) {
	a.Name = name
}

type ArtifactList []*Artifact

// GetByName returns an artifact, if present, by a given name
// Returns an error if not found
func (al ArtifactList) GetByName(name string) (*Artifact, error) {
	for _, artifact := range al {
		if artifact.Name == name {
			return artifact, nil
		}
	}
	return nil, fmt.Errorf("no artifact found with name %s", name)
}

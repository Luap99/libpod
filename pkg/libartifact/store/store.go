//go:build !remote

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/containers/common/libimage"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/oci/layout"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
	"github.com/containers/podman/v5/pkg/libartifact"
	types2 "github.com/containers/podman/v5/pkg/libartifact/types"
	"github.com/containers/storage"
	"github.com/go-openapi/runtime"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	specV1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
)

var (
	// indexName is the name of the JSON file in root of the artifact store
	// that describes the store's contents
	indexName   = "index.json"
	emptyStanza = []byte("{}")
)

type ArtifactStore struct {
	SystemContext *types.SystemContext
	storePath     string
}

// NewArtifactStore is a constructor for artifact stores.  Most artifact dealings depend on this. Store path is
// the filesystem location.
func NewArtifactStore(storePath string, sc *types.SystemContext) (*ArtifactStore, error) {
	// storePath here is an override
	if storePath == "" {
		storeOptions, err := storage.DefaultStoreOptions()
		if err != nil {
			return nil, err
		}
		if storeOptions.GraphRoot == "" {
			return nil, errors.New("unable to determine artifact store")
		}
		storePath = filepath.Join(storeOptions.GraphRoot, "artifacts")
	}

	logrus.Debugf("Using artifact store path: %s", storePath)

	artifactStore := &ArtifactStore{
		storePath:     storePath,
		SystemContext: sc,
	}

	// if the storage dir does not exist, we need to create it.
	baseDir := filepath.Dir(artifactStore.indexPath())
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, err
	}
	// if the index file is not present we need to create an empty one
	_, err := os.Stat(artifactStore.indexPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if createErr := artifactStore.createEmptyManifest(); createErr != nil {
				return nil, createErr
			}
		}
	}
	return artifactStore, nil
}

// Remove an artifact from the local artifact store
func (as ArtifactStore) Remove(ctx context.Context, sys *types.SystemContext, name string) error {
	ir, err := layout.NewReference(as.storePath, name)
	if err != nil {
		return err
	}
	return ir.DeleteImage(ctx, sys)
}

// Inspect an artifact in a local store
func (as ArtifactStore) Inspect(ctx context.Context, name string) (*libartifact.Artifact, error) {
	artifacts, err := as.getArtifacts(ctx, nil)
	if err != nil {
		return nil, err
	}
	return artifacts.GetByName(name)
}

// List artifacts in the local store
func (as ArtifactStore) List(ctx context.Context) (libartifact.ArtifactList, error) {
	return as.getArtifacts(ctx, nil)
}

// Pull an artifact from an image registry to a local store
func (as ArtifactStore) Pull(ctx context.Context, name string, opts libimage.CopyOptions) error {
	srcRef, err := alltransports.ParseImageName(fmt.Sprintf("docker://%s", name))
	if err != nil {
		return err
	}
	destRef, err := layout.NewReference(as.storePath, name)
	if err != nil {
		return err
	}
	copyer, err := libimage.NewCopier(&opts, as.SystemContext, nil)
	if err != nil {
		return err
	}
	_, err = copyer.Copy(ctx, srcRef, destRef)
	if err != nil {
		return err
	}
	return copyer.Close()
}

// Push an artifact to an image registry
func (as ArtifactStore) Push(ctx context.Context, src, dest string, opts libimage.CopyOptions) error {
	destRef, err := alltransports.ParseImageName(fmt.Sprintf("docker://%s", dest))
	if err != nil {
		return err
	}
	srcRef, err := layout.NewReference(as.storePath, src)
	if err != nil {
		return err
	}
	copyer, err := libimage.NewCopier(&opts, as.SystemContext, nil)
	if err != nil {
		return err
	}
	_, err = copyer.Copy(ctx, srcRef, destRef)
	if err != nil {
		return err
	}
	return copyer.Close()
}

// Add takes one or more local files and adds them to the local artifact store.  The empty
// string input is for possible custom artifact types.
func (as ArtifactStore) Add(ctx context.Context, dest string, paths []string, _ string) (*digest.Digest, error) {
	artifactManifestLayers := make([]specV1.Descriptor, 0)

	// Check if artifact already exists
	artifacts, err := as.getArtifacts(ctx, nil)
	if err != nil {
		return nil, err
	}

	// Check if artifact exists; in GetByName not getting an
	// error means it exists
	if _, err := artifacts.GetByName(dest); err == nil {
		return nil, fmt.Errorf("artifact %s already exists", dest)
	}

	ir, err := layout.NewReference(as.storePath, dest)
	if err != nil {
		return nil, err
	}

	imageDest, err := ir.NewImageDestination(ctx, as.SystemContext)
	if err != nil {
		return nil, err
	}

	for _, path := range paths {
		// get the new artifact into the local store
		newBlobDigest, newBlobSize, err := layout.PutBlobFromLocalFile(ctx, imageDest, path)
		if err != nil {
			return nil, err
		}

		newArtifactAnnotations := map[string]string{}
		newArtifactAnnotations[specV1.AnnotationTitle] = filepath.Base(path)
		newLayer := specV1.Descriptor{
			MediaType:   runtime.DefaultMime,
			Digest:      newBlobDigest,
			Size:        newBlobSize,
			Annotations: newArtifactAnnotations,
		}
		artifactManifestLayers = append(artifactManifestLayers, newLayer)
	}

	artifactManifest := specV1.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    specV1.MediaTypeImageManifest,
		ArtifactType: "",
		Config:       specV1.DescriptorEmptyJSON,
		Layers:       artifactManifestLayers,
	}

	rawData, err := json.Marshal(artifactManifest)
	if err != nil {
		return nil, err
	}
	if err := imageDest.PutManifest(ctx, rawData, nil); err != nil {
		return nil, err
	}

	artifactManifestDigest := digest.FromBytes(rawData)

	// the config is an empty JSON stanza i.e. '{}'; if it does not yet exist, it needs
	// to be created
	if err := checkForEmptyStanzaFile(filepath.Join(as.storePath, specV1.ImageBlobsDir, artifactManifestDigest.Algorithm().String(), artifactManifest.Config.Digest.Encoded())); err != nil {
		logrus.Errorf("failed to check or write empty stanza file: %v", err)
	}

	indexAnnotation := map[string]string{}
	indexAnnotation[specV1.AnnotationRefName] = dest
	manifestDescriptor := specV1.Descriptor{
		MediaType:   specV1.MediaTypeImageManifest, // TODO: the media type should be configurable
		Digest:      artifactManifestDigest,
		Size:        int64(len(rawData)),
		Annotations: indexAnnotation,
	}

	// Update the artifact in index.json
	// Begin with reading it
	storeIndex, err := as.readIndex()
	if err != nil {
		return nil, err
	}

	// Update the index.json
	storeIndex.Manifests = append(storeIndex.Manifests, manifestDescriptor)

	// Write index.json
	if err := as.writeIndex(*storeIndex); err != nil {
		return nil, err
	}
	return &artifactManifestDigest, nil
}

func (as ArtifactStore) readIndex() (*specV1.Index, error) {
	index := specV1.Index{}
	rawData, err := os.ReadFile(as.indexPath())
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(rawData, &index)
	return &index, err
}

func (as ArtifactStore) writeIndex(index specV1.Index) error {
	rawData, err := json.Marshal(&index)
	if err != nil {
		return err
	}
	return os.WriteFile(as.indexPath(), rawData, 0o644)
}

func (as ArtifactStore) createEmptyManifest() error {
	index := specV1.Index{}
	rawData, err := json.Marshal(&index)
	if err != nil {
		return err
	}

	return os.WriteFile(as.indexPath(), rawData, 0o644)
}

func (as ArtifactStore) indexPath() string {
	return filepath.Join(as.storePath, indexName)
}

// getArtifacts returns an ArtifactList based on the artifact's store.  The return error and
// unused opts is meant for future growth like filters, etc so the API does not change.
func (as ArtifactStore) getArtifacts(ctx context.Context, _ *types2.GetArtifactOptions) (libartifact.ArtifactList, error) {
	var (
		al libartifact.ArtifactList
	)
	lrs, err := layout.List(as.storePath)
	if err != nil {
		return nil, err
	}
	for _, l := range lrs {
		imgSrc, err := l.Reference.NewImageSource(ctx, as.SystemContext)
		if err != nil {
			return nil, err
		}
		manifests, err := getManifests(ctx, imgSrc, nil)
		if err != nil {
			return nil, err
		}

		artifact := libartifact.Artifact{
			Manifests: manifests,
		}
		if val, ok := l.ManifestDescriptor.Annotations[types2.AnnotatedName]; ok {
			artifact.SetName(val)
		}

		al = append(al, &artifact)
	}
	return al, nil
}

// getManifests takes an imgSrc and starting digest (nil means "top") and collects all the manifests "under"
// it.  this func calls itself recursively with a new startingDigest assuming that we are dealing with
// and index list
func getManifests(ctx context.Context, imgSrc types.ImageSource, startingDigest *digest.Digest) ([]manifest.OCI1, error) {
	var (
		manifests []manifest.OCI1
	)
	b, manifestType, err := imgSrc.GetManifest(ctx, startingDigest)
	if err != nil {
		return nil, err
	}
	// this assumes that there are only single, and multi-images
	if !manifest.MIMETypeIsMultiImage(manifestType) {
		// these are the keepers
		mani, err := manifest.OCI1FromManifest(b)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, *mani)
		return manifests, nil
	}
	// We are dealing with an oci index list
	maniList, err := manifest.OCI1IndexFromManifest(b)
	if err != nil {
		return nil, err
	}
	for _, m := range maniList.Manifests {
		iterManifests, err := getManifests(ctx, imgSrc, &m.Digest)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, iterManifests...)
	}
	return manifests, nil
}

func checkForEmptyStanzaFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, emptyStanza, 0644)
}

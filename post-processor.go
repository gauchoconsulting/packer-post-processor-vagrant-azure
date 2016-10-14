package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	azureStorage "github.com/Azure/azure-sdk-for-go/storage"

	"encoding/base64"
	"github.com/mitchellh/packer/common"
	"github.com/mitchellh/packer/helper/config"
	"github.com/mitchellh/packer/packer"
	"github.com/mitchellh/packer/template/interpolate"
)

type Config struct {
	StorageAccountName  string `mapstructure:"storage_account_name"`
	ContainerName       string `mapstructure:"container_name"`
	AccessKey           string `mapstructure:"access_key"`
	Key                 string `mapstructure:"key"`
	ManifestPath        string `mapstructure:"manifest"`
	BoxName             string `mapstructure:"box_name"`
	BoxDir              string `mapstructure:"box_dir"`
	Version             string `mapstructure:"version"`
	common.PackerConfig `mapstructure:",squash"`

	ctx interpolate.Context
}

type PostProcessor struct {
	config     Config
	blobClient *azureStorage.BlobStorageClient
}

func (p *PostProcessor) Configure(raws ...interface{}) error {
	err := config.Decode(&p.config, &config.DecodeOpts{
		Interpolate:        true,
		InterpolateContext: &p.config.ctx,
		InterpolateFilter: &interpolate.RenderFilter{
			Exclude: []string{"output"},
		},
	}, raws...)
	if err != nil {
		return err
	}

	errs := new(packer.MultiError)

	// required configuration
	templates := map[string]*string{
		"storage_account_name": &p.config.StorageAccountName,
		"container_name":       &p.config.ContainerName,
		"access_key":           &p.config.AccessKey,
		"manifest":             &p.config.ManifestPath,
		"box_name":             &p.config.BoxName,
		"box_dir":              &p.config.BoxDir,
	}

	for key, ptr := range templates {
		if *ptr == "" {
			errs = packer.MultiErrorAppend(errs, fmt.Errorf("vagrant-azure %s must be set", key))
		}
	}

	// Template process
	for key, ptr := range templates {
		if err = interpolate.Validate(*ptr, &p.config.ctx); err != nil {
			errs = packer.MultiErrorAppend(
				errs, fmt.Errorf("Error parsing %s template: %s", key, err))
		}
	}

	storageClient, err := azureStorage.NewBasicClient(p.config.StorageAccountName, p.config.AccessKey)
	if err != nil {
		errs = packer.MultiErrorAppend(errs, fmt.Errorf("Error creating storage client for storage account %q: %s", p.config.StorageAccountName, err))
	}

	blobClient := storageClient.GetBlobService()
	p.blobClient = &blobClient

	if len(errs.Errors) > 0 {
		return errs
	}

	return nil
}

func (p *PostProcessor) PostProcess(ui packer.Ui, artifact packer.Artifact) (packer.Artifact, bool, error) {
	// Only accept input from the vagrant post-processor
	//if artifact.BuilderId() != "mitchellh.post-processor.vagrant" {
	//	return nil, false, fmt.Errorf("Unknown artifact type, requires box from vagrant post-processor: %s", artifact.BuilderId())
	//}

	// Assume there is only one .box file to upload
	box := artifact.Files()[0]
	if !strings.HasSuffix(box, ".box") {
		return nil, false, fmt.Errorf("Unknown files in artifact from vagrant post-processor: %s", artifact.Files())
	}

	provider := providerFromBuilderName(artifact.Id())
	ui.Say(fmt.Sprintf("Preparing to upload box for '%s' provider to azure container '%s'", provider, p.config.ContainerName))

	// determine box size
	boxStat, err := os.Stat(box)
	if err != nil {
		return nil, false, err
	}
	ui.Message(fmt.Sprintf("Box to upload: %s (%d bytes)", box, boxStat.Size()))

	// determine version
	version := p.config.Version

	if version == "" {
		version, err = p.determineVersion()
		if err != nil {
			return nil, false, err
		}

		ui.Message(fmt.Sprintf("No version defined, using %s as new version", version))
	} else {
		ui.Message(fmt.Sprintf("Using %s as new version", version))
	}

	// generate the path to store the box in azure
	boxPath := fmt.Sprintf("%s/%s/%s", p.config.BoxDir, version, path.Base(box))

	ui.Message("Generating checksum")
	checksum, err := sum256(box)
	if err != nil {
		return nil, false, err
	}
	ui.Message(fmt.Sprintf("Checksum is %s", checksum))

	//upload the box to azure
	ui.Message(fmt.Sprintf("Uploading box to azure: %s", boxPath))
	err = p.uploadBox(box, boxPath)

	if err != nil {
		return nil, false, err
	}

	// get the latest manifest so we can add to it
	ui.Message("Fetching latest manifest")
	manifest, err := p.getManifest()
	if err != nil {
		return nil, false, err
	}

	ui.Message(fmt.Sprintf("Adding %s %s box to manifest", provider, version))

	url := p.blobClient.GetBlobURL(p.config.ContainerName, boxPath)

	err = manifest.add(version, &Provider{
		Name:         provider,
		Url:          url,
		ChecksumType: "sha256",
		Checksum:     checksum,
	})
	if err != nil {
		return nil, false, err
	}

	ui.Message(fmt.Sprintf("Uploading the manifest: %s", p.config.ManifestPath))
	if err := p.putManifest(manifest); err != nil {
		return nil, false, err
	}

	return &Artifact{
		Url: p.blobClient.GetBlobURL(p.config.ContainerName, boxPath),
	}, true, nil
}

func (p *PostProcessor) determineVersion() (string, error) {
	manifest, err := p.getManifest()
	if err != nil {
		return "", err
	} else {
		return manifest.getNextVersion(), nil
	}
}

func putBlockBlob(b *azureStorage.BlobStorageClient, container, name string, blob io.Reader, chunkSize int) error {
	if chunkSize <= 0 || chunkSize > azureStorage.MaxBlobBlockSize {
		chunkSize = azureStorage.MaxBlobBlockSize
	}

	chunk := make([]byte, chunkSize)
	n, err := blob.Read(chunk)
	if err != nil && err != io.EOF {
		return err
	}

	blockList := []azureStorage.Block{}

	for blockNum := 0; ; blockNum++ {
		id := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%011d", blockNum)))
		data := chunk[:n]
		err = b.PutBlock(container, name, id, data)
		if err != nil {
			return err
		}

		blockList = append(blockList, azureStorage.Block{ID: id, Status: azureStorage.BlockStatusLatest})

		// Read next block
		n, err = blob.Read(chunk)
		if err != nil && err != io.EOF {
			return err
		}
		if err == io.EOF {
			break
		}
	}

	return b.PutBlockList(container, name, blockList)
}

func (p *PostProcessor) uploadBox(box, boxPath string) error {
	// open the file for reading
	file, err := os.Open(box)
	if err != nil {
		return err
	}
	defer file.Close()

	err = p.blobClient.CreateBlockBlob(p.config.ContainerName, boxPath)
	if err != nil {
		return err
	}

	err = putBlockBlob(p.blobClient, p.config.ContainerName, boxPath, file, azureStorage.MaxBlobBlockSize)

	return err
}

func (p *PostProcessor) getManifest() (*Manifest, error) {

	blob, err := p.blobClient.GetBlob(p.config.ContainerName, p.config.ManifestPath)

	if err != nil {
		if storErr, ok := err.(azureStorage.AzureStorageServiceError); ok {
			if storErr.Code == "BlobNotFound" {
				return &Manifest{Name: p.config.BoxName}, nil
			}
		}
		return nil, err
	}

	defer blob.Close()

	manifest := &Manifest{}
	if err := json.NewDecoder(blob).Decode(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (p *PostProcessor) putManifest(manifest *Manifest) error {
	var buf bytes.Buffer
	err := json.NewEncoder(&buf).Encode(manifest)
	if err != nil {
		return err
	}

	data := buf.String()

	return p.blobClient.CreateBlockBlobFromReader(
		p.config.ContainerName,
		p.config.ManifestPath,
		uint64(len(data)),
		strings.NewReader(data),
		map[string]string{
			"Content-Type": "application/json",
		},
	)
}

// calculates a sha256 checksum of the file
func sum256(filePath string) (string, error) {
	// open the file for reading
	file, err := os.Open(filePath)

	if err != nil {
		return "", err
	}

	defer file.Close()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// converts a packer builder name to the corresponding vagrant provider
func providerFromBuilderName(name string) string {
	switch name {
	case "aws":
		return "aws"
	case "digitalocean":
		return "digitalocean"
	case "virtualbox":
		return "virtualbox"
	case "vmware":
		return "vmware_desktop"
	case "parallels":
		return "parallels"
	default:
		return name
	}
}

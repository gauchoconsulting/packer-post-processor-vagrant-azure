Packer Vagrant azure post-processor
================================

Uploads built Vagrant boxes to azure and manages a manifest file for versioned boxes.

Based of: https://github.com/lmars/packer-post-processor-vagrant-s3

Installation
------------

### Pre-built binaries

The easiest way to install this post-processor is to download a pre-built binary. The builds are hosted 
[here](https://github.com/gauchoconsulting/packer-post-processor-vagrant-azure/releases). Follow the link, download the correct binary for your 
platform, then rename the file to `packer-post-processor-vagrant-azure` and place it in `~/.packer.d/plugins` so 
that Packer can find it (create the directory if it doesn't exist).

### Building from source

You'll need git and go installed for this. First, download the code by running the following command:

```
$ go get github.com/gauchoconsulting/packer-post-processor-vagrant-azure
```
Then, copy the plugin into `~/.packer.d/plugins` directory:

```
$ mkdir $HOME/.packer.d/plugins
$ cp $GOPATH/bin/packer-post-processor-vagrant-azure $HOME/.packer.d/plugins

```
Usage
-----

Add the post-processor to your packer template **after** the `vagrant` post-processor:

```json
{
  "variables": {
    "version":  "0.0.1",
    "box_organization": "my-organization",
    "box_name": "my-cool-project"
  },
  "builders": [ ... ],
  "provisioners": [ ... ],
  "post-processors": [
    [
      {
        "type": "vagrant"
        ...
      },
      {
        "type":     "vagrant-azure",
        "storage_account": "example_storage_account",
        "container_name": "example_container_name",
        "access_key": "super_secret",
        "manifest": "vagrant/json/{{ user `box_organization` }}/{{ user `box_name` }}.json",
        "box_dir":  "vagrant/boxes/{{ user `box_organization` }}/{{ user `box_name` }}",
        "box_name": "{{ user `box_organization` }}{{ user `box_name` }}",
        "version":  "{{ user `version` }}"
      }
    ]
  ]
}
```

**NOTE:** The post-processors must be a **nested array** (i.e.: a Packer sequence definition) so that they run in order. See the [Packer template documentation](http://www.packer.io/docs/templates/post-processors.html) for more information.

When pointing the `Vagrantfile` at a manifest instead of directly at a box you retain traditional features such as
versioning and multiple providers.

Configuration
-------------

All configuration properties are **required**, except where noted.

### storage account

The name of your storage account on azure.

### container name

The name of the container you wish to upload the box into.

### access key

The access key for the storage account you are using.

### manifest

The path to the manifest file in your bucket. If you don't have a manifest, don't worry, one will be created.  **We recommend that you name you manifest the same as your box.**

This controls what users of your box will set `vm.config.box_url` to in their `Vagrantfile` (e.g. `https://storageaccountname.blob.core.windows.net/container/vagrant/manifest.json`).

### box_name

The name of your box.

This is what users of your box will set `vm.config.box` to in their `Vagrantfile`.

### box_dir

The path to a directory in your bucket to store boxes in (e.g. `vagrant/boxes`).

### version (optional)

The version of the box you are uploading. The box will be uploaded to a azure directory path that includes the version number (e.g. `vagrant/boxes/<version>`).

Only one box can be uploaded per provider for a given version. If you are building an updated box, you should bump this version, meaning users of your box will be made aware of the new version.

You may also omit `version` completely, in which case the version will automatically be bumped to the next minor revision (e.g. if you have versions `0.0.1` and `0.0.2` in your manifest the new version will become `0.1.0`).

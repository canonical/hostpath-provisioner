# Hostpath provisioner

This is the hostpath-provisioner used in [MicroK8s](https://microk8s.io) to provide simple hostpath-based storage.

It is based on [the demo hostpath-provisioner from kubernetes-incubator](https://github.com/kubernetes-incubator/external-storage/tree/master/docs/demo/hostpath-provisioner), and contains modifications proposed [here](https://github.com/MaZderMind/hostpath-provisioner).

## Build

[Go](https://golang.org) version 1.17 or newer is required to build this project.

```bash
sudo snap install --classic go
go version
```

After Go has been installed, simply use `make` to build hostpath-provisioner into a single static binary:

```bash
make
```

## Build Docker image

```bash
docker login
make image
make push
```

## Release

Docker images for the hostpath-provisioner are released to [DockerHub](https://hub.docker.com/r/cdkbot/hostpath-provisioner), and they are available for amd64, arm64, s390x architectures.

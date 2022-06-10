# Copyright 2016 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

IMAGE?=cdkbot/hostpath-provisioner
VERSION?=$(shell git rev-parse HEAD | head -c 8)

TAG_VERSION=$(IMAGE):$(VERSION)
TAG_LATEST=$(IMAGE):latest

all: hostpath-provisioner

# Build Docker images
build-images: build-image-amd64 build-image-arm64 build-image-s390x build-image-ppc64le
build-image-%:
	docker build -t ${TAG_VERSION}-$* . --build-arg arch=$*

# Push Docker images
push-images: push-image-amd64 push-image-arm64 push-image-s390x push-image-ppc64le
push-image-%: build-image-%
	docker image push $(TAG_VERSION)-$*

# Push Docker manifests for multi-arch
manifest: manifest-$(VERSION)
manifest-%: push-images
	docker manifest rm $(IMAGE):$* || true
	docker manifest create $(IMAGE):$* --amend $(TAG_VERSION)-amd64 --amend $(TAG_VERSION)-arm64 --amend $(TAG_VERSION)-s390x --amend $(TAG_VERSION)-ppc64le
	docker manifest annotate $(IMAGE):$* $(TAG_VERSION)-amd64 --arch=amd64
	docker manifest annotate $(IMAGE):$* $(TAG_VERSION)-arm64 --arch=arm64
	docker manifest annotate $(IMAGE):$* $(TAG_VERSION)-s390x --arch=s390x
	docker manifest annotate $(IMAGE):$* $(TAG_VERSION)-ppc64le --arch=ppc64le
	docker manifest push $(IMAGE):$*

# Build
hostpath-provisioner: $(shell find . -name "*.go")
	CGO_ENABLED=0 go build -a -ldflags '-extldflags "-static"' -o hostpath-provisioner .

clean:
	rm hostpath-provisioner

.PHONY: all build-images push-images manifests build-image-* push-image-* manifest-*

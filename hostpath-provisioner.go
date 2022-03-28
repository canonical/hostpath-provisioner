/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path"
	"syscall"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/controller"
)

const (
	resyncPeriod = 15 * time.Second
	// The provisioner name "microk8s.io/hostpath" must be the one used in the storage class manifest
	provisionerName           = "microk8s.io/hostpath"
	exponentialBackOffOnError = false
	failedRetryThreshold      = 5
)

var kubeconfigFilePath = os.Getenv("KUBECONFIG")

type hostPathProvisioner struct {
	// The directory to create PV-backing directories in
	pvDir string

	// Identity of this hostPathProvisioner, set to node's name. Used to identify
	// "this" provisioner's PVs.
	identity string

	// Override the default reclaim-policy of dynamicly provisioned volumes
	// (which is remove).
	reclaimPolicy string
}

// NewHostPathProvisioner creates a new hostpath provisioner
func NewHostPathProvisioner() controller.Provisioner {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		klog.Fatal("env variable NODE_NAME must be set so that this provisioner can identify itself")
	}

	pvDir := os.Getenv("PV_DIR")
	if pvDir == "" {
		klog.Fatal("env variable PV_DIR must be set so that this provisioner knows where to place its data")
	}

	reclaimPolicy := os.Getenv("PV_RECLAIM_POLICY")
	return &hostPathProvisioner{
		pvDir:         pvDir,
		identity:      nodeName,
		reclaimPolicy: reclaimPolicy,
	}
}

var _ controller.Provisioner = &hostPathProvisioner{}

// Provision creates a storage asset and returns a PV object representing it.
func (p *hostPathProvisioner) Provision(ctx context.Context, options controller.ProvisionOptions) (*v1.PersistentVolume, controller.ProvisioningState, error) {
	path := path.Join(p.pvDir, options.PVC.Namespace+"-"+options.PVC.Name+"-"+options.PVName)
	klog.Infof("creating backing directory: %v", path)

	if err := os.MkdirAll(path, 0777); err != nil {
		return nil, controller.ProvisioningFinished, err
	}

	reclaimPolicy := *options.StorageClass.ReclaimPolicy
	if p.reclaimPolicy != "" {
		reclaimPolicy = v1.PersistentVolumeReclaimPolicy(p.reclaimPolicy)
	}

	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: options.PVName,
			Annotations: map[string]string{
				"hostPathProvisionerIdentity": p.identity,
			},
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: reclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: path,
				},
			},
		},
	}

	return pv, controller.ProvisioningFinished, nil
}

// Delete removes the storage asset that was created by Provision represented
// by the given PV.
func (p *hostPathProvisioner) Delete(ctx context.Context, volume *v1.PersistentVolume) error {
	ann, ok := volume.Annotations["hostPathProvisionerIdentity"]
	if !ok {
		return errors.New("identity annotation not found on PV")
	}
	if ann != p.identity {
		return &controller.IgnoredError{Reason: "identity annotation on PV does not match ours"}
	}

	path := volume.Spec.PersistentVolumeSource.HostPath.Path
	klog.Info("removing backing directory: %v", path)
	if err := os.RemoveAll(path); err != nil {
		return err
	}

	return nil
}

func main() {
	syscall.Umask(0)

	klog.InitFlags(nil)
	flag.Parse()

	// Use the provided kubeconfig file, or
	var loadConfig func() (*rest.Config, error)
	if kubeconfigFilePath == "" {
		loadConfig = rest.InClusterConfig
	} else {
		loadConfig = func() (*rest.Config, error) {
			return clientcmd.BuildConfigFromFlags("", kubeconfigFilePath)
		}
	}

	config, err := loadConfig()
	if err != nil {
		klog.Fatalf("Failed to create config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create client: %v", err)
	}

	// Create the provisioner: it implements the Provisioner interface expected by
	// the controller
	hostPathProvisioner := NewHostPathProvisioner()

	// Start the provision controller which will dynamically provision hostPath
	// PVs
	pc := controller.NewProvisionController(clientset, provisionerName, hostPathProvisioner,
		controller.ExponentialBackOffOnError(exponentialBackOffOnError),
		controller.ResyncPeriod(resyncPeriod),
		controller.FailedProvisionThreshold(failedRetryThreshold),
		controller.FailedDeleteThreshold(failedRetryThreshold),
	)

	// Never stops.
	pc.Run(context.Background())
}

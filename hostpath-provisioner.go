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
	"fmt"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	klog "k8s.io/klog/v2"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v9/controller"
)

const (
	resyncPeriod = 15 * time.Second
	// The provisioner name must be the one used in the storage class manifest
	provisionerName           = "k8s.canonical.com/hostpath"
	exponentialBackOffOnError = false
	failedRetryThreshold      = 5
	defaultBusyboxImage       = "busybox:1.34.1"
)

var kubeconfigFilePath = os.Getenv("KUBECONFIG")

// podInterface is used to manage pods in a single namespace.
type podInterface interface {
	Create(context.Context, *v1.Pod, metav1.CreateOptions) (*v1.Pod, error)
	Get(context.Context, string, metav1.GetOptions) (*v1.Pod, error)
	Delete(context.Context, string, metav1.DeleteOptions) error
}

type hostPathProvisioner struct {
	// The directory to create PV-backing directories in
	pvDir string

	// Identity of this hostPathProvisioner, set to node's name. Used to identify
	// "this" provisioner's PVs.
	identity string

	// Override the default reclaim-policy of dynamicly provisioned volumes
	// (which is remove).
	reclaimPolicy string

	// pods is a clientset.CoreV1().Pods(namespace)
	pods podInterface

	// busyboxImage is the busybox image to use
	busyboxImage string
}

// NewHostPathProvisioner creates a new hostpath provisioner
func NewHostPathProvisioner(clientset *kubernetes.Clientset) controller.Provisioner {
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		klog.Fatal("env variable NAMESPACE must be set to the namespace of the provisioner")
	}
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		klog.Fatal("env variable NODE_NAME must be set so that this provisioner can identify itself")
	}

	pvDir := os.Getenv("PV_DIR")
	if pvDir == "" {
		klog.Fatal("env variable PV_DIR must be set so that this provisioner knows where to place its data")
	}

	busyboxImage := os.Getenv("BUSYBOX_IMAGE")
	if busyboxImage == "" {
		busyboxImage = defaultBusyboxImage
	}
	klog.Infof("Using busybox image: %q", busyboxImage)

	reclaimPolicy := os.Getenv("PV_RECLAIM_POLICY")
	return &hostPathProvisioner{
		pvDir:         pvDir,
		identity:      nodeName,
		reclaimPolicy: reclaimPolicy,
		pods:          clientset.CoreV1().Pods(namespace),
		busyboxImage:  busyboxImage,
	}
}

var _ controller.Provisioner = &hostPathProvisioner{}

// runOnNode is used to perform provisioning and deleting of pvc directories on any node in the cluster.
func (p *hostPathProvisioner) runOnNode(ctx context.Context, node string, pvDir string, command []string) (*v1.Pod, error) {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("hostpath-provisioner-%s-", node),
			Labels: map[string]string{
				"k8s.hostpath.io/managed-by": p.identity,
			},
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{{
				Name:    "busybox",
				Image:   p.busyboxImage,
				Command: command,
				VolumeMounts: []v1.VolumeMount{{
					Name:      "hostpath-pv-dir",
					MountPath: pvDir,
				}},
				SecurityContext: &v1.SecurityContext{
					RunAsUser: &[]int64{0}[0],
				},
				Resources: v1.ResourceRequirements{
					Limits:   v1.ResourceList{v1.ResourceCPU: resource.MustParse("100m"), v1.ResourceMemory: resource.MustParse("64Mi")},
					Requests: v1.ResourceList{v1.ResourceCPU: resource.MustParse("15m"), v1.ResourceMemory: resource.MustParse("16Mi")},
				},
			}},
			Volumes: []v1.Volume{{
				Name: "hostpath-pv-dir",
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: pvDir,
						Type: &[]v1.HostPathType{"DirectoryOrCreate"}[0],
					},
				},
			}},
		},
	}

	if node != "" {
		pod.Spec.NodeSelector = map[string]string{
			"kubernetes.io/hostname": node,
		}
	}

	createdPod, err := p.pods.Create(ctx, pod, metav1.CreateOptions{
		FieldValidation: "Strict",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}
	klog.V(2).Infof("started pod %s/%s with command %v", createdPod.Namespace, createdPod.Name, createdPod.Spec.Containers[0].Command)

waitLoop:
	for i := 0; i < 100; i++ {
		time.Sleep(5 * time.Second)
		klog.V(1).Infof("(retry %d) waiting for pod: %s", i, createdPod.Name)
		pod, err = p.pods.Get(ctx, createdPod.Name, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("failed to get pod: %s: %s", createdPod.Name, err)
			continue waitLoop
		}

		switch pod.Status.Phase {
		case v1.PodSucceeded:
			klog.Infof("pod completed: %s", createdPod.Name)
			if err := p.pods.Delete(ctx, createdPod.Name, *metav1.NewDeleteOptions(0)); err != nil {
				klog.Warningf("failed to delete pod %s: %s", createdPod.Name, err)
			}
			return pod, nil
		case v1.PodFailed:
			klog.Infof("pod failed: %s", createdPod.Name)
			if err := p.pods.Delete(ctx, createdPod.Name, *metav1.NewDeleteOptions(0)); err != nil {
				klog.Warningf("failed to delete pod %s: %s", createdPod.Name, err)
			}
			return pod, fmt.Errorf("pod failed: %s", createdPod.Name)
		default:
			klog.V(1).Infof("(retry %d) pod %s: %s", createdPod.Name, pod.Status.Phase)
			continue waitLoop
		}
	}

	return pod, nil
}

// Provision creates a storage asset and returns a PV object representing it.
func (p *hostPathProvisioner) Provision(ctx context.Context, options controller.ProvisionOptions) (*v1.PersistentVolume, controller.ProvisioningState, error) {
	pvDir := p.pvDir
	if storageClassPvDir, ok := options.StorageClass.Parameters["pvDir"]; ok {
		pvDir = storageClassPvDir
	}
	path := path.Join(pvDir, fmt.Sprintf("%s-%s-%s", options.PVC.Namespace, options.PVC.Name, options.PVName))
	klog.Infof("creating backing directory: %v", path)

	var selectedNodeName string
	if node := options.SelectedNode; node != nil {
		selectedNodeName = node.Name
	} else {
		klog.Warningf("persistent volume for %q does not have a selected node, this could cause node affinity issues. you can avoid these issues by setting \"VolumeBindingMode: WaitForFirstConsumer\" on the storage class", options.PVC.Name)
	}
	pod, err := p.runOnNode(ctx, selectedNodeName, pvDir, []string{"mkdir", "-m", "0777", "-p", path})
	if err != nil {
		klog.Infof("failed to create backing directory: %s", err)
		return nil, controller.ProvisioningFinished, fmt.Errorf("failed to create backing directory: %s", err)
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
					Type: &[]v1.HostPathType{"DirectoryOrCreate"}[0],
				},
			},
			NodeAffinity: &v1.VolumeNodeAffinity{
				Required: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{{
						MatchExpressions: []v1.NodeSelectorRequirement{{
							Key:      "kubernetes.io/hostname",
							Operator: "In",
							Values:   []string{pod.Spec.NodeName},
						}},
					}},
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

	// find node from NodeAffinity
	var node string
	if affinity := volume.Spec.NodeAffinity; affinity != nil {
		if required := affinity.Required; required != nil {
			if terms := required.NodeSelectorTerms; len(terms) > 0 && len(terms[0].MatchExpressions) > 0 {
				if expr := terms[0].MatchExpressions[0]; expr.Key == "kubernetes.io/hostname" && expr.Operator == "In" && len(expr.Values) == 1 {
					node = expr.Values[0]
				}
			}
		}
	}
	if node == "" {
		klog.Warningf("could not find node for volume: %s", volume.Name)
		return nil
	}

	path := volume.Spec.PersistentVolumeSource.HostPath.Path
	klog.Infof("removing backing directory: %s", path)
	if _, err := p.runOnNode(ctx, node, filepath.Dir(path), []string{"rm", "-rf", path}); err != nil {
		klog.Warningf("failed to remove backing directory: %s", path)
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
	hostPathProvisioner := NewHostPathProvisioner(clientset)

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

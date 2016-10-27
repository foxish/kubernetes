/*
Copyright 2016 The Kubernetes Authors.

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

package e2e

import (
	"fmt"
	"net"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/api"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/watch"
	testutils "k8s.io/kubernetes/test/utils"
)


type Address struct {
	internalIP string
	externalIP string
	hostname   string
}

// Blocks outgoing network traffic on 'node'. Then verifies that 'podNameToDisappear',
// that belongs to replication controller 'rcName', really disappeared.
// Finally, it checks that the replication controller recreates the
// pods on another node and that now the number of replicas is equal 'replicas'.
// At the end (even in case of errors), the network traffic is brought back to normal.
// This function executes commands on a node so it will work only for some
// environments.
func performTemporaryNetworkFailure(c clientset.Interface, ns, rcName string, replicas int32, podNameToDisappear string, node *api.Node) {
	host := getNodeExternalIP(node)
	master := getMasterAddress(c)
	By(fmt.Sprintf("block network traffic from node %s to the master", node.Name))
	defer func() {
		// This code will execute even if setting the iptables rule failed.
		// It is on purpose because we may have an error even if the new rule
		// had been inserted. (yes, we could look at the error code and ssh error
		// separately, but I prefer to stay on the safe side).
		By(fmt.Sprintf("Unblock network traffic from node %s to the master", node.Name))
		framework.UnblockNetwork(host, master)
	}()

	framework.Logf("Waiting %v to ensure node %s is ready before beginning test...", resizeNodeReadyTimeout, node.Name)
	if !framework.WaitForNodeToBe(c, node.Name, api.NodeReady, true, resizeNodeReadyTimeout) {
		framework.Failf("Node %s did not become ready within %v", node.Name, resizeNodeReadyTimeout)
	}
	framework.BlockNetwork(host, master)

	framework.Logf("Waiting %v for node %s to be not ready after simulated network failure", resizeNodeNotReadyTimeout, node.Name)
	if !framework.WaitForNodeToBe(c, node.Name, api.NodeReady, false, resizeNodeNotReadyTimeout) {
		framework.Failf("Node %s did not become not-ready within %v", node.Name, resizeNodeNotReadyTimeout)
	}

	framework.Logf("Waiting for pod %s to be removed", podNameToDisappear)
	err := framework.WaitForRCPodToDisappear(c, ns, rcName, podNameToDisappear)
	Expect(err).NotTo(HaveOccurred())

	By("verifying whether the pod from the unreachable node is recreated")
	err = framework.VerifyPods(c, ns, rcName, true, replicas)
	Expect(err).NotTo(HaveOccurred())

	// network traffic is unblocked in a deferred function
}

// getMasterAddress returns the hostname/external IP/internal IP as appropriate for e2e tests on a particular provider
// which is the address of the interface used for communication with the kubelet.
func getMasterAddress(c clientset.Interface) string {
	master := getMaster(c)
	switch framework.TestContext.Provider {
	case "gce", "gke":
		return master.externalIP
	case "aws":
		// TODO(justinsb): Avoid hardcoding this.
		return "172.20.0.9"
	default:
		framework.Failf("This test is not supported for provider %s and should be disabled", framework.TestContext.Provider)
	}
	return ""
}

// getMaster populates the externalIP, internalIP and hostname fields of the master.
// If any of these is unavailable, it is set to "".
func getMaster(c clientset.Interface) Address {
	master := Address{}

	// Populate the internal IP.
	eps, err := c.Core().Endpoints(api.NamespaceDefault).Get("kubernetes")
	if err != nil {
		framework.Failf("Failed to get kubernetes endpoints: %v", err)
	}
	if len(eps.Subsets) != 1 || len(eps.Subsets[0].Addresses) != 1 {
		framework.Failf("There are more than 1 endpoints for kubernetes service: %+v", eps)
	}
	master.internalIP = eps.Subsets[0].Addresses[0].IP

	// Populate the external IP/hostname.
	host := strings.TrimPrefix(framework.TestContext.Host, "https://")
	if net.ParseIP(host) != nil {
		master.externalIP = host
	} else {
		master.hostname = host
	}

	return master
}

func expectNodeReadiness(isReady bool, newNode chan *api.Node) {
	timeout := false
	expected := false
	timer := time.After(nodeReadinessTimeout)
	for !expected && !timeout {
		select {
		case n := <-newNode:
			if framework.IsNodeConditionSetAsExpected(n, api.NodeReady, isReady) {
				expected = true
			} else {
				framework.Logf("Observed node ready status is NOT %v as expected", isReady)
			}
		case <-timer:
			timeout = true
		}
	}
	if !expected {
		framework.Failf("Failed to observe node ready status change to %v", isReady)
	}
}

// Return node external IP concatenated with port 22 for ssh
// e.g. 1.2.3.4:22
func getNodeExternalIP(node *api.Node) string {
	framework.Logf("Getting external IP address for %s", node.Name)
	host := ""
	for _, a := range node.Status.Addresses {
		if a.Type == api.NodeExternalIP {
			host = a.Address + ":22"
			break
		}
	}
	if host == "" {
		framework.Failf("Couldn't get the external IP of host %s with addresses %v", node.Name, node.Status.Addresses)
	}
	return host
}

func newPodOnNode(c clientset.Interface, namespace, podName, nodeName string) error {
	pod, err := c.Core().Pods(namespace).Create(podOnNode(podName, nodeName, serveHostnameImage))
	if err == nil {
		framework.Logf("Created pod %s on node %s", pod.ObjectMeta.Name, nodeName)
	} else {
		framework.Logf("Failed to create pod %s on node %s: %v", podName, nodeName, err)
	}
	return err
}

func podOnNode(podName, nodeName string, image string) *api.Pod {
	return &api.Pod{
		ObjectMeta: api.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"name": podName,
			},
		},
		Spec: api.PodSpec{
			Containers: []api.Container{
				{
					Name:  podName,
					Image: image,
					Ports: []api.ContainerPort{{ContainerPort: 9376}},
				},
			},
			NodeName:      nodeName,
			RestartPolicy: api.RestartPolicyNever,
		},
	}
}


var _ = framework.KubeDescribe("Network [Disruptive]", func() {
	f := framework.NewDefaultFramework("network-partition")
	var systemPodsNo int32
	var c clientset.Interface
	var ns string
	ignoreLabels := framework.ImagePullerLabels
	var group string

	BeforeEach(func() {
		c = f.ClientSet
		ns = f.Namespace.Name
		systemPods, err := framework.GetPodsInNamespace(c, ns, ignoreLabels)
		Expect(err).NotTo(HaveOccurred())
		systemPodsNo = int32(len(systemPods))
		if strings.Index(framework.TestContext.CloudConfig.NodeInstanceGroup, ",") >= 0 {
			framework.Failf("Test does not support cluster setup with more than one MIG: %s", framework.TestContext.CloudConfig.NodeInstanceGroup)
		} else {
			group = framework.TestContext.CloudConfig.NodeInstanceGroup
		}
	})
	framework.KubeDescribe("Network", func() {
		Context("when a node becomes unreachable", func() {
			BeforeEach(func() {
				framework.SkipUnlessProviderIs("gce", "gke", "aws")
				framework.SkipUnlessNodeCountIsAtLeast(2)
			})

			// TODO marekbiskup 2015-06-19 #10085
			// This test has nothing to do with resizing nodes so it should be moved elsewhere.
			// Two things are tested here:
			// 1. pods from a uncontactable nodes are rescheduled
			// 2. when a node joins the cluster, it can host new pods.
			// Factor out the cases into two separate tests.
			It("[replication controller] recreates pods scheduled on the unreachable node "+
				"AND allows scheduling of pods on a node after it rejoins the cluster", func() {

				// Create a replication controller for a service that serves its hostname.
				// The source for the Docker container kubernetes/serve_hostname is in contrib/for-demos/serve_hostname
				name := "my-hostname-net"
				newSVCByName(c, ns, name)
				replicas := int32(framework.TestContext.CloudConfig.NumNodes)
				newRCByName(c, ns, name, replicas)
				err := framework.VerifyPods(c, ns, name, true, replicas)
				Expect(err).NotTo(HaveOccurred(), "Each pod should start running and responding")

				By("choose a node with at least one pod - we will block some network traffic on this node")
				label := labels.SelectorFromSet(labels.Set(map[string]string{"name": name}))
				options := api.ListOptions{LabelSelector: label}
				pods, err := c.Core().Pods(ns).List(options) // list pods after all have been scheduled
				Expect(err).NotTo(HaveOccurred())
				nodeName := pods.Items[0].Spec.NodeName

				node, err := c.Core().Nodes().Get(nodeName)
				Expect(err).NotTo(HaveOccurred())

				By(fmt.Sprintf("block network traffic from node %s", node.Name))
				performTemporaryNetworkFailure(c, ns, name, replicas, pods.Items[0].Name, node)
				framework.Logf("Waiting %v for node %s to be ready once temporary network failure ends", resizeNodeReadyTimeout, node.Name)
				if !framework.WaitForNodeToBeReady(c, node.Name, resizeNodeReadyTimeout) {
					framework.Failf("Node %s did not become ready within %v", node.Name, resizeNodeReadyTimeout)
				}

				// sleep a bit, to allow Watch in NodeController to catch up.
				time.Sleep(5 * time.Second)

				By("verify whether new pods can be created on the re-attached node")
				// increasing the RC size is not a valid way to test this
				// since we have no guarantees the pod will be scheduled on our node.
				additionalPod := "additionalpod"
				err = newPodOnNode(c, ns, additionalPod, node.Name)
				Expect(err).NotTo(HaveOccurred())
				err = framework.VerifyPods(c, ns, additionalPod, true, 1)
				Expect(err).NotTo(HaveOccurred())

				// verify that it is really on the requested node
				{
					pod, err := c.Core().Pods(ns).Get(additionalPod)
					Expect(err).NotTo(HaveOccurred())
					if pod.Spec.NodeName != node.Name {
						framework.Logf("Pod %s found on invalid node: %s instead of %s", pod.Name, pod.Spec.NodeName, node.Name)
					}
				}
			})

			// What happens in this test:
			// 	Network traffic from a node to master is cut off to simulate network partition
			// Expect to observe:
			// 1. Node is marked NotReady after timeout by nodecontroller (40seconds)
			// 2. All pods on node are marked NotReady shortly after #1
			// 3. Node and pods return to Ready after connectivivty recovers
			It("All pods on the unreachable node should be marked as NotReady upon the node turn NotReady "+
				"AND all pods should be mark back to Ready when the node get back to Ready before pod eviction timeout", func() {
				By("choose a node - we will block all network traffic on this node")
				var podOpts api.ListOptions
				nodeOpts := api.ListOptions{}
				nodes, err := c.Core().Nodes().List(nodeOpts)
				Expect(err).NotTo(HaveOccurred())
				framework.FilterNodes(nodes, func(node api.Node) bool {
					if !framework.IsNodeConditionSetAsExpected(&node, api.NodeReady, true) {
						return false
					}
					podOpts = api.ListOptions{FieldSelector: fields.OneTermEqualSelector(api.PodHostField, node.Name)}
					pods, err := c.Core().Pods(api.NamespaceAll).List(podOpts)
					if err != nil || len(pods.Items) <= 0 {
						return false
					}
					return true
				})
				if len(nodes.Items) <= 0 {
					framework.Failf("No eligible node were found: %d", len(nodes.Items))
				}
				node := nodes.Items[0]
				podOpts = api.ListOptions{FieldSelector: fields.OneTermEqualSelector(api.PodHostField, node.Name)}
				if err = framework.WaitForMatchPodsCondition(c, podOpts, "Running and Ready", podReadyTimeout, testutils.PodRunningReady); err != nil {
					framework.Failf("Pods on node %s are not ready and running within %v: %v", node.Name, podReadyTimeout, err)
				}

				By("Set up watch on node status")
				nodeSelector := fields.OneTermEqualSelector("metadata.name", node.Name)
				stopCh := make(chan struct{})
				newNode := make(chan *api.Node)
				var controller *cache.Controller
				_, controller = cache.NewInformer(
					&cache.ListWatch{
						ListFunc: func(options api.ListOptions) (runtime.Object, error) {
							options.FieldSelector = nodeSelector
							obj, err := f.ClientSet.Core().Nodes().List(options)
							return runtime.Object(obj), err
						},
						WatchFunc: func(options api.ListOptions) (watch.Interface, error) {
							options.FieldSelector = nodeSelector
							return f.ClientSet.Core().Nodes().Watch(options)
						},
					},
					&api.Node{},
					0,
					cache.ResourceEventHandlerFuncs{
						UpdateFunc: func(oldObj, newObj interface{}) {
							n, ok := newObj.(*api.Node)
							Expect(ok).To(Equal(true))
							newNode <- n

						},
					},
				)

				defer func() {
					// Will not explicitly close newNode channel here due to
					// race condition where stopCh and newNode are closed but informer onUpdate still executes.
					close(stopCh)
				}()
				go controller.Run(stopCh)

				By(fmt.Sprintf("Block traffic from node %s to the master", node.Name))
				host := getNodeExternalIP(&node)
				master := getMasterAddress(c)
				defer func() {
					By(fmt.Sprintf("Unblock traffic from node %s to the master", node.Name))
					framework.UnblockNetwork(host, master)

					if CurrentGinkgoTestDescription().Failed {
						return
					}

					By("Expect to observe node and pod status change from NotReady to Ready after network connectivity recovers")
					expectNodeReadiness(true, newNode)
					if err = framework.WaitForMatchPodsCondition(c, podOpts, "Running and Ready", podReadyTimeout, testutils.PodRunningReady); err != nil {
						framework.Failf("Pods on node %s did not become ready and running within %v: %v", node.Name, podReadyTimeout, err)
					}
				}()

				framework.BlockNetwork(host, master)

				By("Expect to observe node and pod status change from Ready to NotReady after network partition")
				expectNodeReadiness(false, newNode)
				if err = framework.WaitForMatchPodsCondition(c, podOpts, "NotReady", podNotReadyTimeout, testutils.PodNotReady); err != nil {
					framework.Failf("Pods on node %s did not become NotReady within %v: %v", node.Name, podNotReadyTimeout, err)
				}
			})
		})
	})
})

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

package batch

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"
	"k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	kubeapiserveradmission "k8s.io/kubernetes/pkg/kubeapiserver/admission"
)

type BatchJobSpec struct {
	Foo string `json:"foo"`
	Bar bool   `json:"bar"`
}

type BatchJob struct {
	metav1.TypeMeta `json:",inline"`
	Metadata        metav1.ObjectMeta `json:"metadata"`

	Spec BatchJobSpec `json:"spec"`
}

type BatchJobList struct {
	metav1.TypeMeta `json:",inline"`
	Metadata        metav1.ListMeta `json:"metadata"`

	Items []BatchJob `json:"items"`
}

func init() {
	admission.RegisterPlugin("DelayedBatch", func(config io.Reader) (admission.Interface, error) {
		return NewDelayedBatch(), nil
	})
}

// plugin contains the client used by the admission controller
type plugin struct {
	*admission.Handler
	client internalclientset.Interface
}

// NewDelayedBatch creates a new instance of the LimitPodHardAntiAffinityTopology admission controller
func NewDelayedBatch() admission.Interface {
	return &plugin{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}
}

var _ = kubeapiserveradmission.WantsInternalKubeClientSet(&plugin{})

func (p *plugin) SetInternalKubeClientSet(client internalclientset.Interface) {
	p.client = client
}

func (p *plugin) CreateTPR() {
	// initialize third party resource if it does not exist
	tpr, err := p.client.Extensions().ThirdPartyResources().Get("batch-job.kubernetes.io", metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			tpr := &extensions.ThirdPartyResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "batch-job.kubernetes.io",
				},
				Versions: []extensions.APIVersion{
					{Name: "v1"},
				},
				Description: "A batch-job template",
			}

			result, err := p.client.Extensions().ThirdPartyResources().Create(tpr)
			if err != nil {
				panic(err)
			}
			fmt.Printf("CREATED: %#v\nFROM: %#v\n", result, tpr)
		} else {
			panic(err)
		}
	} else {
		fmt.Printf("SKIPPING: already exists %#v\n", tpr)
	}

	groupversion := schema.GroupVersion{
		Group:   "kubernetes.io",
		Version: "v1",
	}

	schemeBuilder := runtime.NewSchemeBuilder(
		func(scheme *runtime.Scheme) error {
			scheme.AddKnownTypes(
				groupversion,
				&BatchJob{},
				&BatchJobList{},
			)
			return nil
		})
	metav1.AddToGroupVersion(api.Scheme, groupversion)
	schemeBuilder.AddToScheme(api.Scheme)
}

func (p *plugin) CreateTPRInstance(podName string) {
	// Create an instance of our TPR
	glog.Infof("CREATE TPR INSTANCE!")

	example := &BatchJob{
		Metadata: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: BatchJobSpec{
			Foo: "hello",
			Bar: true,
		},
	}
	b, err := json.Marshal(example)
	if err != nil {
		panic(err)
	}

	var result BatchJob



	req := p.client.Core().RESTClient().Post().Body(b)
	req = req.AbsPath("/apis/kubernetes.io/v1/namespaces/default/batchjobs")
	req.Do().Into(&result)
	if err != nil {
		glog.Errorf("Could not create batch as expected! - %+v", err)
	}
	fmt.Printf("CREATED: %#v\n", result)
}

// Admit will print the pod name to apiserver log!
// instead, stuff things into a TPR right here, for delayed submission.
func (p *plugin) Admit(attributes admission.Attributes) (err error) {
	// Ignore all calls to subresources or resources other than pods.
	if len(attributes.GetSubresource()) != 0 || attributes.GetResource().GroupResource() != api.Resource("pods") {
		return nil
	}
	pod, ok := attributes.GetObject().(*api.Pod)
	if !ok {
		return apierrors.NewBadRequest("Resource was marked with kind Pod but was unable to be converted")
	}
	p.CreateTPR()
	if pod.Annotations["batch"] == "true" {
		p.CreateTPRInstance(pod.Name)
		return fmt.Errorf("Scheduled Pod %v in queue Y\n", pod.Name)
	}
	return nil
}

func (p *plugin) Validate() error {
	if p.client == nil {
		return fmt.Errorf("missing client")
	}
	return nil
}

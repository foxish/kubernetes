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
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/kubernetes/pkg/api"
)

func init() {
	admission.RegisterPlugin("DelayedBatch", func(config io.Reader) (admission.Interface, error) {
		return NewDelayedBatch(), nil
	})
}

// plugin contains the client used by the admission controller
type plugin struct {
	*admission.Handler
}

// NewDelayedBatch creates a new instance of the LimitPodHardAntiAffinityTopology admission controller
func NewDelayedBatch() admission.Interface {
	return &plugin{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}
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




	return fmt.Errorf("## Name of pod admitted by DelayedBatch = %+v", pod.Name)
}

/*
Copyright 2020 The Kubernetes Authors.

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

package pkg

import (
	"bytes"
	"flag"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/metadata"
	metadatafake "k8s.io/client-go/metadata/fake"
	coretesting "k8s.io/client-go/testing"
	klog "k8s.io/klog/v2"
)

func TestVerify(t *testing.T) {
	gcVerbs := []string{"get", "list", "delete"}

	v1Resources := &metav1.APIResourceList{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "nodes", Namespaced: false, Kind: "Node", Verbs: gcVerbs},
			{Name: "pods", Namespaced: true, Kind: "Pod", Verbs: gcVerbs},
		},
	}

	addObject := func(t *testing.T, metadataClient *metadatafake.FakeMetadataClient, apiVersion, resource, kind, name, namespace, uid string, owners ...metav1.OwnerReference) {
		t.Helper()
		groupVersion, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			t.Fatal(err)
		}
		resourceClient := metadataClient.Resource(groupVersion.WithResource(resource))
		var objectClient metadata.ResourceInterface
		if len(namespace) > 0 {
			objectClient = resourceClient.Namespace(namespace)
		} else {
			objectClient = resourceClient
		}
		_, err = objectClient.(metadatafake.MetadataClient).CreateFake(
			&metav1.PartialObjectMetadata{
				TypeMeta:   metav1.TypeMeta{APIVersion: apiVersion, Kind: kind},
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid), OwnerReferences: owners},
			}, metav1.CreateOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	testcases := []struct {
		name string

		resources            []*metav1.APIResourceList
		adjustMetadataClient func(*metadatafake.FakeMetadataClient)

		expectOut string
		expectErr string
	}{
		{
			name:      "simple",
			resources: []*metav1.APIResourceList{v1Resources},
			expectOut: ``,
			expectErr: `
				fetching v1, nodes
				got 1 item
				fetching v1, pods
				got 1 item
				No invalid ownerReferences found
			`,
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "v1", "nodes", "Node", "node1", "", "node1uid")
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1",
					metav1.OwnerReference{APIVersion: "v1", Kind: "Node", Name: "node1", UID: types.UID("node1uid")},
				)
			},
		},
		{
			name: "forbidden",
			resources: []*metav1.APIResourceList{
				v1Resources,
				{
					GroupVersion: "forbidden/v1",
					APIResources: []metav1.APIResource{{Name: "forbiddenresources", Namespaced: true, Kind: "ForbiddenKind", Verbs: gcVerbs}},
				},
			},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "v1", "nodes", "Node", "node1", "", "node1uid")
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1",
					metav1.OwnerReference{APIVersion: "v1", Kind: "Node", Name: "node1", UID: types.UID("node1uid")},
					metav1.OwnerReference{APIVersion: "forbidden/v1", Kind: "ForbiddenKind", Name: "forbiddenparent", UID: types.UID("forbiddenparentuid")},
				)
				metadataClient.PrependReactor("list", "forbiddenresources", func(action coretesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "forbiddenresources"}, "", fmt.Errorf("not authorized"))
				})
			},
			expectOut: `
			GROUP   RESOURCE   NAMESPACE   NAME   OWNER_UID            LEVEL     MESSAGE
			        pods       ns1         pod1   forbiddenparentuid   Warning   could not list parent resource forbiddenresources.forbidden
			`,
			expectErr: `
			fetching v1, nodes
            got 1 item
            fetching v1, pods
            got 1 item
            fetching forbidden/v1, forbiddenresources
            warning: could not list forbidden/v1, Resource=forbiddenresources: forbiddenresources is forbidden: not authorized
            0 errors, 2 warnings
			`,
		},
		{
			name: "unavailable",
			resources: []*metav1.APIResourceList{v1Resources,
				{
					GroupVersion: "unavailable/v1",
					APIResources: []metav1.APIResource{{Name: "unavailableresources", Namespaced: true, Kind: "UnavailableKind", Verbs: gcVerbs}},
				},
			},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "v1", "nodes", "Node", "node1", "", "node1uid")
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1",
					metav1.OwnerReference{APIVersion: "v1", Kind: "Node", Name: "node1", UID: types.UID("node1uid")},
					metav1.OwnerReference{APIVersion: "unavailable/v1", Kind: "UnavailableKind", Name: "unavailableparent", UID: types.UID("unavailableparentuid")},
				)
				metadataClient.PrependReactor("list", "unavailableresources", func(action coretesting.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, apierrors.NewServiceUnavailable("server is unavailable")
				})
			},
			expectOut: `
			GROUP   RESOURCE   NAMESPACE   NAME   OWNER_UID              LEVEL     MESSAGE
			        pods       ns1         pod1   unavailableparentuid   Warning   could not list parent resource unavailableresources.unavailable
			`,
			expectErr: `
			fetching v1, nodes
            got 1 item
            fetching v1, pods
            got 1 item
            fetching unavailable/v1, unavailableresources
            warning: could not list unavailable/v1, Resource=unavailableresources: server is unavailable
            0 errors, 2 warnings
			`,
		},
		{
			name:      "unavailable version",
			resources: []*metav1.APIResourceList{v1Resources},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "v1", "nodes", "Node", "node1", "", "node1uid")
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1",
					metav1.OwnerReference{APIVersion: "v2", Kind: "Node", Name: "node1", UID: types.UID("node1uid")},
				)
			},
			expectOut: `
			GROUP   RESOURCE   NAMESPACE   NAME   OWNER_UID   LEVEL   MESSAGE
			        pods       ns1         pod1   node1uid    Error   cannot resolve owner apiVersion/kind: no matches for kind "Node" in version "v2"
			`,
			expectErr: `
			fetching v1, nodes
            got 1 item
            fetching v1, pods
            got 1 item
            1 error, 0 warnings
			`,
		},
		{
			name:      "mismatched name",
			resources: []*metav1.APIResourceList{v1Resources},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "v1", "nodes", "Node", "node1", "", "node1uid")
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1",
					metav1.OwnerReference{APIVersion: "v1", Kind: "Node", Name: "nodex", UID: types.UID("node1uid")},
				)
			},
			expectOut: `
			GROUP   RESOURCE   NAMESPACE   NAME   OWNER_UID   LEVEL   MESSAGE
			        pods       ns1         pod1   node1uid    Error   ownerReference name (nodex) does not match owner name (node1)
			`,
			expectErr: `
			fetching v1, nodes
            got 1 item
            fetching v1, pods
            got 1 item
            1 error, 0 warnings
			`,
		},
		{
			name:      "mismatched kind",
			resources: []*metav1.APIResourceList{v1Resources},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "v1", "nodes", "Node", "node1", "", "node1uid")
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1",
					metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: "node1", UID: types.UID("node1uid")},
				)
			},
			expectOut: `
			GROUP   RESOURCE   NAMESPACE   NAME   OWNER_UID   LEVEL   MESSAGE
			        pods       ns1         pod1   node1uid    Error   ownerReference group/kind (/Pod) does not match owner group/kind (/Node)
			`,
			expectErr: `
			fetching v1, nodes
            got 1 item
            fetching v1, pods
            got 1 item
            1 error, 0 warnings
			`,
		},
		{
			name:      "cluster child, namespaced owner",
			resources: []*metav1.APIResourceList{v1Resources},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "v1", "nodes", "Node", "node1", "", "node1uid",
					metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: "pod1", UID: types.UID("poduid1")},
				)
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1")
			},
			expectOut: `
			GROUP   RESOURCE   NAMESPACE   NAME    OWNER_UID   LEVEL   MESSAGE
			        nodes                  node1   poduid1     Error   cannot reference namespaced type as owner (apiVersion=v1,kind=Pod)
			`,
			expectErr: `
			fetching v1, nodes
            got 1 item
            fetching v1, pods
            got 1 item
            1 error, 0 warnings
			`,
		},
		{
			name:      "mismatched namespace",
			resources: []*metav1.APIResourceList{v1Resources},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod2", "ns2", "poduid2",
					metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: "pod1", UID: types.UID("poduid1")},
				)
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1")
			},
			expectOut: `
			GROUP   RESOURCE   NAMESPACE   NAME   OWNER_UID   LEVEL   MESSAGE
			        pods       ns2         pod2   poduid1     Error   child namespace does not match owner namespace (ns1)
			`,
			expectErr: `
			fetching v1, nodes
            got 0 items
            fetching v1, pods
            got 2 items
            1 error, 0 warnings
			`,
		},
		{
			name: "multigroup object",
			resources: []*metav1.APIResourceList{
				v1Resources,
				{
					GroupVersion: "group1/v1",
					APIResources: []metav1.APIResource{{Name: "multigroupresources", Namespaced: true, Kind: "MultiGroupKind", Verbs: gcVerbs}},
				},
				{
					GroupVersion: "group2/v1beta1",
					APIResources: []metav1.APIResource{{Name: "multigroupresources", Namespaced: true, Kind: "MultiGroupKind", Verbs: gcVerbs}},
				},
			},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "group1/v1", "multigroupresources", "MultiGroupKind", "mgr1", "ns1", "mgruid1")
				addObject(t, metadataClient, "group2/v1beta1", "multigroupresources", "MultiGroupKind", "mgr1", "ns1", "mgruid1")
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1",
					metav1.OwnerReference{APIVersion: "group2/v1beta1", Kind: "MultiGroupKind", Name: "mgr1", UID: types.UID("mgruid1")},
				)
			},
			expectOut: ``,
			expectErr: `
			fetching v1, nodes
            got 0 items
            fetching v1, pods
			got 1 item
			fetching group1/v1, multigroupresources
			got 1 item
			fetching group2/v1beta1, multigroupresources
			got 1 item
            No invalid ownerReferences found
			`,
		},
		{
			name: "non-preferred version",
			resources: []*metav1.APIResourceList{
				v1Resources,
				{
					GroupVersion: "group1/v1",
					APIResources: []metav1.APIResource{{Name: "multiversionresources", Namespaced: true, Kind: "MultiVersionKind", Verbs: gcVerbs}},
				},
				{
					GroupVersion: "group1/v1beta1",
					APIResources: []metav1.APIResource{{Name: "multiversionresources", Namespaced: true, Kind: "MultiVersionKind", Verbs: gcVerbs}},
				},
			},
			adjustMetadataClient: func(metadataClient *metadatafake.FakeMetadataClient) {
				addObject(t, metadataClient, "group1/v1", "multiversionresources", "MultiVersionKind", "mgr1", "ns1", "mgruid1")
				addObject(t, metadataClient, "v1", "pods", "Pod", "pod1", "ns1", "poduid1",
					metav1.OwnerReference{APIVersion: "group1/v1beta1", Kind: "MultiVersionKind", Name: "mgr1", UID: types.UID("mgruid1")},
				)
			},
			expectOut: ``,
			expectErr: `
			fetching v1, nodes
            got 0 items
            fetching v1, pods
			got 1 item
			fetching group1/v1, multiversionresources
			got 1 item
            No invalid ownerReferences found
			`,
		},
	}

	klog.InitFlags(nil)
	flag.Set("v", "3")
	if !klog.V(3).Enabled() {
		t.Fatal("expected --v=3 or above")
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			out := bytes.NewBuffer(nil)
			err := bytes.NewBuffer(nil)
			scheme := runtime.NewScheme()

			discoveryClient := &fake.FakeDiscovery{Fake: &coretesting.Fake{}}
			discoveryClient.Resources = tc.resources

			metadataClient := metadatafake.NewSimpleMetadataClient(scheme)
			if tc.adjustMetadataClient != nil {
				tc.adjustMetadataClient(metadataClient)
			}

			opts := &VerifyGCOptions{
				DiscoveryClient: discoveryClient,
				MetadataClient:  metadataClient,
				Stdout:          out,
				Stderr:          err,
			}
			if err := opts.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := opts.Run(); err != nil {
				t.Fatal(err)
			}
			if e, a := normalize(tc.expectOut), normalize(out.String()); !reflect.DeepEqual(e, a) {
				t.Log("stdout:\n" + out.String())
				t.Errorf("unexpected stdout diff:\n%s", cmp.Diff(e, a))
			}
			if e, a := normalize(tc.expectErr), normalize(err.String()); !reflect.DeepEqual(e, a) {
				t.Log("stderr:\n" + err.String())
				t.Errorf("unexpected stderr diff:\n%s", cmp.Diff(e, a))
			}
		})
	}
}

func normalize(in string) []string {
	normalized := regexp.MustCompile("[ \t]+").ReplaceAllString(in, " ")
	trimmed := strings.TrimSpace(normalized)
	split := strings.Split(trimmed, "\n")
	for i := range split {
		split[i] = strings.TrimSpace(split[i])
	}
	return split
}

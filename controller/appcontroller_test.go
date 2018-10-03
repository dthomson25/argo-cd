package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ghodss/yaml"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"

	argoappv1 "github.com/argoproj/argo-cd/pkg/apis/application/v1alpha1"
	appclientset "github.com/argoproj/argo-cd/pkg/client/clientset/versioned/fake"
	reposerver "github.com/argoproj/argo-cd/reposerver/mocks"
	"github.com/stretchr/testify/assert"
)

func newFakeController(kubectl mockKubectlCmd, apps ...runtime.Object) *ApplicationController {
	kubeClientset := fake.NewSimpleClientset()
	appClientset := appclientset.NewSimpleClientset(apps...)
	repoClientset := reposerver.Clientset{}
	return NewApplicationController(
		"argocd",
		kubeClientset,
		kubectl,
		appClientset,
		&repoClientset,
		time.Minute,
	)
}

var fakeApp = `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-app
  namespace: argocd
spec:
  destination:
    namespace: dummy-namespace
    server: https://localhost:6443
  project: default
  source:
    path: some/path
    repoURL: https://github.com/argoproj/argocd-example-apps.git
  syncPolicy:
    automated: {}
status:
  operationState:
    finishedAt: 2018-09-21T23:50:29Z
    message: successfully synced
    operation:
      sync:
        revision: HEAD
    phase: Succeeded
    startedAt: 2018-09-21T23:50:25Z
    syncResult:
      resources:
      - kind: RoleBinding
        message: |-
          rolebinding.rbac.authorization.k8s.io/always-outofsync reconciled
          rolebinding.rbac.authorization.k8s.io/always-outofsync configured
        name: always-outofsync
        namespace: default
        status: Synced
      revision: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`

func newFakeApp() *argoappv1.Application {
	var app argoappv1.Application
	err := yaml.Unmarshal([]byte(fakeApp), &app)
	if err != nil {
		panic(err)
	}
	return &app
}

func TestAutoSync(t *testing.T) {
	app := newFakeApp()
	ctrl := newFakeController(mockKubectlCmd{}, app)
	compRes := argoappv1.ComparisonResult{
		Status:   argoappv1.ComparisonStatusOutOfSync,
		Revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	cond := ctrl.autoSync(app, &compRes)
	assert.Nil(t, cond)
	app, err := ctrl.applicationClientset.ArgoprojV1alpha1().Applications("argocd").Get("my-app", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, app.Operation)
	assert.NotNil(t, app.Operation.Sync)
	assert.False(t, app.Operation.Sync.Prune)
}

func TestSkipAutoSync(t *testing.T) {
	// Verify we skip when we previously synced to it in our most recent history
	// Set current to 'aaaaa', desired to 'aaaa' and mark system OutOfSync
	app := newFakeApp()
	ctrl := newFakeController(mockKubectlCmd{}, app)
	compRes := argoappv1.ComparisonResult{
		Status:   argoappv1.ComparisonStatusOutOfSync,
		Revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	cond := ctrl.autoSync(app, &compRes)
	assert.Nil(t, cond)
	app, err := ctrl.applicationClientset.ArgoprojV1alpha1().Applications("argocd").Get("my-app", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Nil(t, app.Operation)

	// Verify we skip when we are already Synced (even if revision is different)
	app = newFakeApp()
	ctrl = newFakeController(mockKubectlCmd{}, app)
	compRes = argoappv1.ComparisonResult{
		Status:   argoappv1.ComparisonStatusSynced,
		Revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	cond = ctrl.autoSync(app, &compRes)
	assert.Nil(t, cond)
	app, err = ctrl.applicationClientset.ArgoprojV1alpha1().Applications("argocd").Get("my-app", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Nil(t, app.Operation)

	// Verify we skip when auto-sync is disabled
	app = newFakeApp()
	app.Spec.SyncPolicy = nil
	ctrl = newFakeController(mockKubectlCmd{}, app)
	compRes = argoappv1.ComparisonResult{
		Status:   argoappv1.ComparisonStatusOutOfSync,
		Revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	cond = ctrl.autoSync(app, &compRes)
	assert.Nil(t, cond)
	app, err = ctrl.applicationClientset.ArgoprojV1alpha1().Applications("argocd").Get("my-app", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Nil(t, app.Operation)

	// Verify we skip when previous sync attempt failed and return error condition
	// Set current to 'aaaaa', desired to 'bbbbb' and add 'bbbbb' to failure history
	app = newFakeApp()
	app.Status.OperationState = &argoappv1.OperationState{
		Operation: argoappv1.Operation{
			Sync: &argoappv1.SyncOperation{},
		},
		Phase: argoappv1.OperationFailed,
		SyncResult: &argoappv1.SyncOperationResult{
			Revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	ctrl = newFakeController(mockKubectlCmd{}, app)
	compRes = argoappv1.ComparisonResult{
		Status:   argoappv1.ComparisonStatusOutOfSync,
		Revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	cond = ctrl.autoSync(app, &compRes)
	assert.NotNil(t, cond)
	app, err = ctrl.applicationClientset.ArgoprojV1alpha1().Applications("argocd").Get("my-app", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Nil(t, app.Operation)
}

// TestAutoSyncIndicateError verifies we skip auto-sync and return error condition if previous sync failed
func TestAutoSyncIndicateError(t *testing.T) {
	app := newFakeApp()
	app.Spec.Source.ComponentParameterOverrides = []argoappv1.ComponentParameter{
		{
			Name:  "a",
			Value: "1",
		},
	}
	ctrl := newFakeController(mockKubectlCmd{}, app)
	compRes := argoappv1.ComparisonResult{
		Status:   argoappv1.ComparisonStatusOutOfSync,
		Revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	app.Status.OperationState = &argoappv1.OperationState{
		Operation: argoappv1.Operation{
			Sync: &argoappv1.SyncOperation{
				ParameterOverrides: argoappv1.ParameterOverrides{
					{
						Name:  "a",
						Value: "1",
					},
				},
			},
		},
		Phase: argoappv1.OperationFailed,
		SyncResult: &argoappv1.SyncOperationResult{
			Revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	cond := ctrl.autoSync(app, &compRes)
	assert.NotNil(t, cond)
	app, err := ctrl.applicationClientset.ArgoprojV1alpha1().Applications("argocd").Get("my-app", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Nil(t, app.Operation)
}

// TestAutoSyncParameterOverrides verifies we auto-sync if revision is same but parameter overrides are different
func TestAutoSyncParameterOverrides(t *testing.T) {
	app := newFakeApp()
	app.Spec.Source.ComponentParameterOverrides = []argoappv1.ComponentParameter{
		{
			Name:  "a",
			Value: "1",
		},
	}
	ctrl := newFakeController(mockKubectlCmd{}, app)
	compRes := argoappv1.ComparisonResult{
		Status:   argoappv1.ComparisonStatusOutOfSync,
		Revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	app.Status.OperationState = &argoappv1.OperationState{
		Operation: argoappv1.Operation{
			Sync: &argoappv1.SyncOperation{
				ParameterOverrides: argoappv1.ParameterOverrides{
					{
						Name:  "a",
						Value: "2", // this value changed
					},
				},
			},
		},
		Phase: argoappv1.OperationFailed,
		SyncResult: &argoappv1.SyncOperationResult{
			Revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	cond := ctrl.autoSync(app, &compRes)
	assert.Nil(t, cond)
	app, err := ctrl.applicationClientset.ArgoprojV1alpha1().Applications("argocd").Get("my-app", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, app.Operation)
}

func TestRetryUntilSucceedSuccessfull(t *testing.T) {
	refreshRate := 1 * time.Second
	counter := 0
	ctx := context.Background()
	retryUntilSucceed(func() error {
		if counter > 1 {
			return nil
		}
		counter++
		return fmt.Errorf("Counter isn't greater than 1")
	}, "Test Retrying Until Successful return", ctx, refreshRate)
	assert.Equal(t, 2, counter)
}

func TestRetryUntilSucceedCancelContext(t *testing.T) {
	refreshRate := 1 * time.Second
	ctx := context.Background()
	go retryUntilSucceed(func() error {
		return fmt.Errorf("Never return without error")
	}, "Test Retrying Until Successful return", ctx, refreshRate)
	ctx.Done()
}

const crdYaml = `
apiVersion: apiextensions.k8s.io/v1beta1
kind: CustomResourceDefinition
metadata:
  name: test
spec:
  group: example
  versions:
    - name: v1
      served: true
      storage: true
  scope: Namespaced
  names:
    kind: Test
`

const crdInstanceYaml = `
apiVersion: extensions/v1beta1
kind: Test
metadata:
  name: test-instance
`

func TestWatchClusterResource(t *testing.T) {
	var crd unstructured.Unstructured
	err := yaml.Unmarshal([]byte(crdYaml), &crd)
	assert.NoError(t, err)

	var crdInstance unstructured.Unstructured
	err = yaml.Unmarshal([]byte(crdInstanceYaml), &crdInstance)
	assert.NoError(t, err)
	ch := make(chan watch.Event, 2)

	ch <- watch.Event{Type: watch.Added, Object: &crd}
	ch <- watch.Event{Type: watch.Added, Object: &crdInstance}
	close(ch)

	app := newFakeApp()
	ctrl := newFakeController(mockKubectlCmd{events: ch}, app)
	ctx := context.Background()
	err = ctrl.watchClusterResources(ctx, argoappv1.Cluster{})
	assert.EqualError(t, err, "Restarting the watch because a new CRD was added.")
	err = ctrl.watchClusterResources(ctx, argoappv1.Cluster{})
	assert.EqualError(t, err, "resource updates channel has closed")

}

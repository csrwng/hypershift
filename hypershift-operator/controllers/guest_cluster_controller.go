package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	hyperv1 "openshift.io/hypershift/api/v1alpha1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type GuestClusterReconciler struct {
	ctrlclient.Client
	recorder record.EventRecorder
	Infra    *configv1.Infrastructure
	Log      logr.Logger
}

func (r *GuestClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// TODO (alberto): watch hostedControlPlane events too.
	// So when controlPlane.Status.Ready it triggers a reconcile here.
	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&hyperv1.GuestCluster{}).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewItemExponentialFailureRateLimiter(1*time.Second, 10*time.Second),
		}).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "failed setting up with a controller manager")
	}

	var infra configv1.Infrastructure
	if err := mgr.GetAPIReader().Get(context.Background(), client.ObjectKey{Name: "cluster"}, &infra); err != nil {
		return fmt.Errorf("failed to get cluster infra: %w", err)
	}
	r.Infra = &infra

	r.recorder = mgr.GetEventRecorderFor("guest-cluster-controller")

	return nil
}

func (r *GuestClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log = ctrl.LoggerFrom(ctx)
	r.Log.Info("Reconciling")

	// Fetch the GuestCluster instance
	guestCluster := &hyperv1.GuestCluster{}
	err := r.Client.Get(ctx, req.NamespacedName, guestCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.Log.Info("GuestCluster not found")
			return ctrl.Result{}, nil
		}
		r.Log.Error(err, "error getting guestCluster")
		return ctrl.Result{}, err
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, guestCluster.ObjectMeta)
	if err != nil {
		r.Log.Error(err, "error getting owner cluster")
		return ctrl.Result{}, err
	}
	if cluster == nil {
		r.Log.Info("Cluster Controller has not yet set OwnerRef")
		return ctrl.Result{}, nil
	}

	if util.IsPaused(cluster, guestCluster) {
		r.Log.Info("GuestCluster or linked Cluster is marked as paused. Won't reconcile")
		return ctrl.Result{}, nil
	}

	// Return early if deleted
	if !guestCluster.DeletionTimestamp.IsZero() {
		if err := r.delete(ctx, req.Name); err != nil {
			r.Log.Error(err, "failed to delete cluster")
			return ctrl.Result{}, err
		}
		if controllerutil.ContainsFinalizer(guestCluster, finalizer) {
			controllerutil.RemoveFinalizer(guestCluster, finalizer)
			if err := r.Update(ctx, guestCluster); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from cluster: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure the cluster has a finalizer for cleanup
	if !controllerutil.ContainsFinalizer(guestCluster, finalizer) {
		controllerutil.AddFinalizer(guestCluster, finalizer)
		if err := r.Update(ctx, guestCluster); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer to hostedControlPlane: %w", err)
		}
	}
	r.Log = r.Log.WithValues("cluster", cluster.Name)

	patchHelper, err := patch.NewHelper(guestCluster, r.Client)
	if err != nil {
		r.Log.Error(err, "error building patchHelper")
		return ctrl.Result{}, err
	}

	hcp := &hyperv1.HostedControlPlane{}
	controlPlaneRef := types.NamespacedName{
		Name:      cluster.Spec.ControlPlaneRef.Name,
		Namespace: cluster.Namespace,
	}

	if err := r.Client.Get(ctx, controlPlaneRef, hcp); err != nil {
		r.Log.Error(err, "failed to get control plane ref")
		return reconcile.Result{}, err
	}

	// TODO (alberto): populate the API and create/consume infrastructure via aws sdk
	// role profile, sg, vpc, subnets.
	if !hcp.Status.Ready {
		r.Log.Info("Control plane is not ready yet. Requeuing")
		return reconcile.Result{Requeue: true}, nil
	}

	// Create a machineset for the new cluster's worker nodes
	machineSet, err := generateWorkerMachineset(r, ctx, r.Infra.Status.InfrastructureName, guestCluster.GetName(), guestCluster.Spec.ComputeReplicas)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to generate worker machineset: %w", err)
	}
	if err := r.Create(ctx, machineSet); err != nil && !apierrors.IsAlreadyExists(err) {
		return reconcile.Result{}, fmt.Errorf("failed to create machineset: %w", err)
	}
	// TODO (alberto): we currently use openshift mapi which has no notion of remote cluster
	// therefore the machine.status does not get a nodeRef even though it yields a node for the dataplane.
	// Once we move to capi machines we should wait for the machine to get a nodeRef.

	// Set the values for upper level controller
	guestCluster.Status.Ready = true
	guestCluster.Spec.ControlPlaneEndpoint = hyperv1.APIEndpoint{
		Host: hcp.Status.ControlPlaneEndpoint.Host,
		Port: hcp.Status.ControlPlaneEndpoint.Port,
	}

	if err := patchHelper.Patch(ctx, guestCluster); err != nil {
		r.Log.Error(err, "failed to patch")
		return ctrl.Result{}, fmt.Errorf("failed to patch: %w", err)
	}

	r.Log.Info("Successfully reconciled")
	return ctrl.Result{}, nil
}

func generateWorkerMachineset(client ctrlclient.Client, ctx context.Context, infraName string, namespace string, workerCount int) (*unstructured.Unstructured, error) {
	machineSets := &unstructured.UnstructuredList{}
	machineSets.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "machine.openshift.io",
		Version: "v1beta1",
		Kind:    "MachineSet",
	})
	if err := client.List(ctx, machineSets, ctrlclient.InNamespace("openshift-machine-api")); err != nil {
		return nil, fmt.Errorf("failed to list machinesets: %w", err)
	}
	if len(machineSets.Items) == 0 {
		return nil, fmt.Errorf("no machinesets found")
	}
	obj := machineSets.Items[0]

	workerName := generateMachineSetName(infraName, namespace, "worker")
	object := obj.Object

	unstructured.RemoveNestedField(object, "status")
	unstructured.RemoveNestedField(object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(object, "metadata", "generation")
	unstructured.RemoveNestedField(object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(object, "metadata", "selfLink")
	unstructured.RemoveNestedField(object, "metadata", "uid")
	unstructured.RemoveNestedField(object, "spec", "template", "spec", "metadata")
	unstructured.RemoveNestedField(object, "spec", "template", "spec", "providerSpec", "value", "publicIp")
	unstructured.SetNestedField(object, int64(workerCount), "spec", "replicas")
	unstructured.SetNestedField(object, workerName, "metadata", "name")
	unstructured.SetNestedField(object, workerName, "spec", "selector", "matchLabels", "machine.openshift.io/cluster-api-machineset")
	unstructured.SetNestedField(object, workerName, "spec", "template", "metadata", "labels", "machine.openshift.io/cluster-api-machineset")
	unstructured.SetNestedField(object, fmt.Sprintf("%s-user-data", namespace), "spec", "template", "spec", "providerSpec", "value", "userDataSecret", "name")

	return &obj, nil
}

func (r *GuestClusterReconciler) delete(ctx context.Context, name string) error {
	machineSetName := generateMachineSetName(r.Infra.Status.InfrastructureName, name, "worker")
	machineSet := &unstructured.Unstructured{}
	machineSet.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "machine.openshift.io",
		Version: "v1beta1",
		Kind:    "MachineSet",
	})
	machineSet.SetNamespace("openshift-machine-api")
	machineSet.SetName(machineSetName)
	if err := waitForDeletion(ctx, r.Log, r.Client, machineSet); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete machineset %s: %w", machineSetName, err)
	}
	r.Log.Info("deleted machineset", "name", machineSetName)
	return nil
}

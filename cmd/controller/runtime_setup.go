package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openbaov1alpha1 "github.com/kubebao/openbao-operator/api/v1alpha1"
	"github.com/kubebao/openbao-operator/internal/adapter/auth"
	"github.com/kubebao/openbao-operator/internal/adapter/openbao"
	"github.com/kubebao/openbao-operator/internal/adapter/raft"
	"github.com/kubebao/openbao-operator/internal/adapter/security"
	appopenbaocluster "github.com/kubebao/openbao-operator/internal/app/openbaocluster"
	appopenbaorestore "github.com/kubebao/openbao-operator/internal/app/openbaorestore"
	openbaoclustercontroller "github.com/kubebao/openbao-operator/internal/controller/openbaocluster"
	openbaorestorecontroller "github.com/kubebao/openbao-operator/internal/controller/openbaorestore"
	"github.com/kubebao/openbao-operator/internal/platform/admission"
	"github.com/kubebao/openbao-operator/internal/platform/entrypoint"
	portauth "github.com/kubebao/openbao-operator/internal/port/auth"
	portopenbao "github.com/kubebao/openbao-operator/internal/port/openbao"
	certmanager "github.com/kubebao/openbao-operator/internal/service/certs"
	"github.com/kubebao/openbao-operator/internal/service/configuration"
	initmanager "github.com/kubebao/openbao-operator/internal/service/init"
	"github.com/kubebao/openbao-operator/internal/service/restore"
)

type controllerProcessRuntime struct {
	operatorNamespace        string
	platform                 string
	restoreRecoveryClientFor restore.RecoveryClientFactory
	singleTenantMode         bool
	admissionTracker         *admission.Tracker
	oidcRuntime              appopenbaocluster.RuntimeOIDCConfig
	openBaoRuntime           appopenbaocluster.RuntimeOpenBaoConfig
	imageVerificationRuntime appopenbaocluster.RuntimeImageVerificationConfig
}

func buildControllerProcessRuntime(
	ctx context.Context,
	mgr ctrl.Manager,
	cfg runConfig,
	platform string,
	singleTenantMode bool,
) (controllerProcessRuntime, error) {
	config := mgr.GetConfig()
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return controllerProcessRuntime{}, fmt.Errorf("unable to create Kubernetes clientset: %w", err)
	}

	reloadSignaler := certmanager.NewKubernetesReloadSignaler(clientset)
	smartClientConfig := portopenbao.ClientConfig{
		RateLimitQPS:                   cfg.clientQPS,
		RateLimitBurst:                 cfg.clientBurst,
		CircuitBreakerFailureThreshold: cfg.clientCBFailureThreshold,
		CircuitBreakerOpenDuration:     cfg.clientCBOpenDuration,
		JWTAuthStrategy:                cfg.jwtAuthStrategy,
	}

	clientMgr := openbao.NewClientManager(smartClientConfig)
	if err := addOpenBaoClientStateCleanup(mgr, clientMgr, watchNamespaceFromEnv()); err != nil {
		return controllerProcessRuntime{}, err
	}
	tokens := auth.NewControllerTokenSource(clientset)
	raftMgr := raft.NewManager(clientset, raftClientFactoryProvider{clientManager: clientMgr}, tokens)
	initMgr, err := initmanager.NewManager(
		config,
		clientset,
		clientMgr,
		raftMgr,
		mgr.GetEventRecorder(controllerNameOpenBaoCluster),
	)
	if err != nil {
		return controllerProcessRuntime{}, fmt.Errorf("unable to create initialization manager: %w", err)
	}
	imageVerifier := security.NewImageVerifier(mgr.GetLogger().WithName("image-verifier"), mgr.GetAPIReader(), nil)
	operatorImageVerifier := security.NewImageVerifier(
		mgr.GetLogger().WithName("operator-image-verifier"),
		mgr.GetAPIReader(),
		nil,
	)

	operatorNamespace := operatorNamespaceFromEnv()
	if missingHelperImages := unavailableHelperImageDefaultFields(); len(missingHelperImages) > 0 {
		setupLog.Info(
			"Operator-managed default helper images are unavailable until OPERATOR_VERSION is configured; "+
				"clusters can still override helper images explicitly in the cluster spec",
			"fields",
			missingHelperImages,
		)
	}

	oidcConfig := discoverStartupOIDC(ctx, config)
	admissionTracker, err := initializeAdmissionTracker(
		ctx, mgr.GetAPIReader(), cfg.admissionEnforcement, cfg.admissionStartupTimeout,
	)
	if err != nil {
		return controllerProcessRuntime{}, err
	}
	clientFactory := func(config portopenbao.ClientConfig) (portopenbao.ClusterActions, error) {
		return openbao.NewClient(config)
	}

	return controllerProcessRuntime{
		operatorNamespace:        operatorNamespace,
		platform:                 platform,
		restoreRecoveryClientFor: restoreRecoveryClientFactory(clientset, clientMgr, tokens),
		singleTenantMode:         singleTenantMode,
		admissionTracker:         admissionTracker,
		oidcRuntime: appopenbaocluster.RuntimeOIDCConfig{
			Issuer:              oidcConfig.IssuerURL,
			DiscoveryURL:        oidcConfig.OIDCDiscoveryURL,
			DiscoveryCAPEM:      oidcConfig.OIDCDiscoveryCAPEM,
			JWKSURL:             oidcConfig.JWKSURL,
			JWKSCAPEM:           oidcConfig.JWKSCAPEM,
			JWTKeys:             oidcConfig.JWKSKeys,
			Discover:            openBaoClusterOIDCDiscoverer(auth.DiscoverConfig),
			DiscoveryStatusCode: portauth.DiscoveryStatusCode,
		},
		openBaoRuntime: appopenbaocluster.RuntimeOpenBaoConfig{
			TLSReload:   reloadSignaler,
			InitManager: initMgr,
			PolicyManager: &configuration.PolicyManager{
				ClientFor: operatorPolicyClientFactory(clientset, clientMgr, tokens),
			},
			Raft:              raftMgr,
			SmartClientConfig: smartClientConfig,
			ClientForPod: openBaoClusterPodClientFactory(
				mgr.GetClient(),
				smartClientConfig,
				clientFactory,
			),
		},
		imageVerificationRuntime: openBaoClusterImageVerificationRuntime(imageVerifier, operatorImageVerifier),
	}, nil
}

func setupControllers(ctx context.Context, mgr ctrl.Manager, runtime controllerProcessRuntime) error {
	if runtime.openBaoRuntime.Raft == nil {
		return fmt.Errorf("OpenBaoCluster Raft runtime is required")
	}
	if runtime.openBaoRuntime.InitManager == nil {
		return fmt.Errorf("OpenBaoCluster initialization manager is required")
	}

	applications := buildOpenBaoClusterApplications(mgr, runtime)
	if err := (&openbaoclustercontroller.OpenBaoClusterReconciler{
		Client: mgr.GetClient(),
		ControllerRuntime: openbaoclustercontroller.ControllerRuntime{
			APIReader:        mgr.GetAPIReader(),
			AdmissionTracker: runtime.admissionTracker,
			Recorder:         mgr.GetEventRecorder(controllerNameOpenBaoCluster),
			SingleTenantMode: runtime.singleTenantMode,
		},
		Applications: applications,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller %s: %w", controllerNameOpenBaoCluster, err)
	}

	if err := (&openbaorestorecontroller.OpenBaoRestoreReconciler{
		Client:           mgr.GetClient(),
		AdmissionTracker: runtime.admissionTracker,
		RestoreReconciler: appopenbaorestore.NewRestoreReconciler(appopenbaorestore.RestoreDependencies{
			Client:                mgr.GetClient(),
			APIReader:             mgr.GetAPIReader(),
			Scheme:                mgr.GetScheme(),
			Recorder:              mgr.GetEventRecorder(controllerNameOpenBaoRestore),
			OperatorImageVerifier: runtime.imageVerificationRuntime.OperatorImageVerifier,
			Platform:              runtime.platform,
			ClientConfig:          runtime.openBaoRuntime.SmartClientConfig,
			RecoveryClientFor:     runtime.restoreRecoveryClientFor,
		}),
	}).SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("unable to create controller %s: %w", controllerNameOpenBaoRestore, err)
	}

	return nil
}

func addManagerHealthChecks(ctx context.Context, mgr ctrl.Manager, singleTenantMode bool) error {
	// Match the watches in OpenBaoCluster and OpenBaoRestore SetupWithManager.
	watchedObjects := []client.Object{&openbaov1alpha1.OpenBaoCluster{}, &openbaov1alpha1.OpenBaoRestore{}}
	if singleTenantMode {
		watchedObjects = append(watchedObjects,
			&appsv1.StatefulSet{}, &corev1.Service{}, &corev1.ConfigMap{},
			&corev1.Secret{}, &batchv1.Job{}, &corev1.ServiceAccount{},
		)
	}
	return entrypoint.AddManagerHealthChecks(ctx, mgr, watchedObjects...)
}

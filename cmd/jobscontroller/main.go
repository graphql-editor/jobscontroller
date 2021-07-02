package main

import (
	"context"
	"flag"
	"time"

	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	// Uncomment the following line to load the gcp plugin (only required to authenticate against GKE clusters).
	// _ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	"github.com/graphql-editor/jobscontroller/pkg/controllers"
	clientset "github.com/graphql-editor/jobscontroller/pkg/generated/clientset/versioned"
	informers "github.com/graphql-editor/jobscontroller/pkg/generated/informers/externalversions"
	"github.com/graphql-editor/jobscontroller/pkg/signals"
)

var (
	masterURL  string
	kubeconfig string
	namespace  string
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// set up signals so we handle the first shutdown signal gracefully
	stopCh := signals.SetupSignalHandler()

	cfg, err := clientcmd.BuildConfigFromFlags(masterURL, kubeconfig)
	if err != nil {
		klog.Fatalf("Error building kubeconfig: %s", err.Error())
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building kubernetes clientset: %s", err.Error())
	}

	jobscontrollerClientset, err := clientset.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("Error building jobscontroller clientset: %s", err.Error())
	}
	var kiopts []kubeinformers.SharedInformerOption
	var jcopts []informers.SharedInformerOption
	if namespace != "" {
		kiopts = append(kiopts, kubeinformers.WithNamespace(namespace))
		jcopts = append(jcopts, informers.WithNamespace(namespace))
	}

	kubeInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(kubeClient, time.Second*30, kiopts...)
	jobsInformerFactory := informers.NewSharedInformerFactoryWithOptions(jobscontrollerClientset, time.Second*30, jcopts...)

	controller := controllers.NewController(kubeClient, jobscontrollerClientset,
		kubeInformerFactory.Core().V1().Pods(),
		kubeInformerFactory.Batch().V1().Jobs(),
		jobsInformerFactory.Jobscontroller().V1alpha1().FunctionJobs())

	// notice that there is no need to run Start methods in a separate goroutine. (i.e. go kubeInformerFactory.Start(stopCh)
	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	kubeInformerFactory.Start(stopCh)
	jobsInformerFactory.Start(stopCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stopCh
		cancel()
	}()
	if err = controller.Run(ctx, 2); err != nil {
		klog.Fatalf("Error running controller: %s", err.Error())
	}
}

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&masterURL, "master", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&namespace, "namespace", "", "Limit controller to a namespace.")
}
